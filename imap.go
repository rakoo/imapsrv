// An IMAP server
package unpeu

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/rakoo/unpeu/auth"
)

// DefaultListener is the listener that is used if no listener is specified
const DefaultListener = "0.0.0.0:143"

// config is an IMAP server configuration
type config struct {
	maxClients uint
	listeners  []listener
	mailstore  Mailstore

	authBackend auth.AuthStore
}

type Option func(*Server) error

// listener represents a listener as used by the server
type listener struct {
	addr         string
	encryption   encryptionLevel
	certificates []tls.Certificate
	listener     net.Listener
}

// Server is an IMAP Server
type Server struct {
	// Server configuration
	config *config
	// Number of active clients
	activeClients uint

	// context object to signal end of life
	done chan struct{}
}

// client is an IMAP Client as seen by an IMAP server
type client struct {
	// conn is the lowest-level connection layer
	conn net.Conn
	// listener refers to the listener that's handling this client
	listener listener

	bufin  *bufio.Reader
	bufout *bufio.Writer
	id     string
	config *config
}

// defaultConfig returns the default server configuration
func defaultConfig() *config {
	return &config{
		listeners:  make([]listener, 0, 4),
		maxClients: 8,
	}
}

// Add a mailstore to the config
// StoreOption add a mailstore to the config
func StoreOption(m Mailstore) Option {
	return func(s *Server) error {
		s.config.mailstore = m
		return nil
	}
}

// AuthStoreOption adds an authenticaton backend
func AuthStoreOption(a auth.AuthStore) Option {
	return func(s *Server) error {
		s.config.authBackend = a
		return nil
	}
}

// ListenOption adds an interface to listen to
func ListenOption(Addr string) Option {
	return func(s *Server) error {
		l := listener{
			addr: Addr,
		}
		s.config.listeners = append(s.config.listeners, l)
		return nil
	}
}

// ListenSTARTTLSOption enables STARTTLS with the given certificate and keyfile
func ListenSTARTTLSOption(Addr, certFile, keyFile string) Option {
	return func(s *Server) error {
		// Load the ceritificates
		var err error
		certs := make([]tls.Certificate, 1)
		certs[0], err = tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return err
		}

		// Set up the listener
		l := listener{
			addr:         Addr,
			encryption:   starttlsLevel,
			certificates: certs,
		}
		s.config.listeners = append(s.config.listeners, l)
		return nil
	}
}

// MaxClientsOption sets the MaxClients config
func MaxClientsOption(max uint) Option {
	return func(s *Server) error {
		s.config.maxClients = max
		return nil
	}
}

// NewServer creates a new server with the given options
func NewServer(options ...Option) *Server {
	// set the default config
	s := &Server{
		done: make(chan struct{}),
	}
	s.config = defaultConfig()

	// override the config with the functional options
	for _, option := range options {
		err := option(s)
		if err != nil {
			log.Fatal(err)
		}
	}

	return s
}

// Start an IMAP server
func (s *Server) Start() error {
	// Use a default listener if none exist
	if len(s.config.listeners) == 0 {
		s.config.listeners = append(s.config.listeners,
			listener{addr: DefaultListener})
	}
	if s.config.authBackend == nil {
		s.config.authBackend = auth.DummyAuthBackend{}
	}
	if s.config.mailstore == nil {
		log.Fatal("Can't run without a mailstore")
	}

	var err error
	// Start listening for IMAP connections
	for i, iface := range s.config.listeners {
		s.config.listeners[i].listener, err = net.Listen("tcp", iface.addr)
		if err != nil {
			log.Printf("IMAP cannot listen on %s, %v", iface.addr, err)
			return err
		}
	}

	// Start the server on each port
	n := len(s.config.listeners)
	for i := 0; i < n; i += 1 {
		listener := s.config.listeners[i]
		go s.runListener(s.done, listener, i)
	}

	return nil
}

func (s *Server) Stop() {
	close(s.done)
}

// runListener runs the given listener on a separate goroutine
func (s *Server) runListener(done chan struct{}, listener listener, id int) {

	log.Printf("IMAP server %d listening on %s", id, listener.listener.Addr().String())

	newClient := make(chan *client)
	go func() {
		clientNumber := 1
		for {
			// Accept a connection from a new client
			conn, err := listener.listener.Accept()
			if err != nil {
				log.Print("IMAP accept error, ", err)
				continue
			}

			// Handle the client
			client := &client{
				conn:     conn,
				listener: listener,
				bufin:    bufio.NewReader(conn),
				bufout:   bufio.NewWriter(conn),
				// TODO: perhaps we can do this without Sprint, maybe strconv.Itoa()
				id:     fmt.Sprint(id, "/", clientNumber),
				config: s.config,
			}
			clientNumber++
			newClient <- client
		}
	}()

	for {
		select {
		case <-s.done:
			return
		case client := <-newClient:
			go client.handle(s)
		}
	}

}

// handle requests from an IMAP client
func (c *client) handle(s *Server) {

	// Close the client on exit from this function
	defer c.close()

	// Create a parser
	parser := createParser(c.bufin)

	// Write the welcome message
	err := ok("*", "IMAP4rev1 Service Ready").write(c.bufout)

	if err != nil {
		c.logError(err)
		return
	}

	//  Create a session
	sess := createSession(c.id, c.config, s, &c.listener, c.conn)

	for {
		// Get the next IMAP command
		command, err := parser.next()
		if err != nil {
			if err != io.EOF {
				c.logError(fmt.Errorf("Couldn't get next command: %s", err))
			}
			fatalResponse(c.bufout, fmt.Errorf("Invalid input"))
			return
		}

		for {
			// Execute the IMAP command
			response := command.execute(sess)

			// Possibly replace buffers (layering)
			if response.bufReplacement != nil {
				c.bufout = response.bufReplacement.W
				c.bufin = response.bufReplacement.R
				parser.lexer.reader = &response.bufReplacement.Reader
			}

			// Write back the response
			err = response.write(c.bufout)

			if err != nil {
				c.logError(err)
				return
			}

			// Should the connection be closed?
			if response.closeConnection {
				return
			}
			if response.done {
				break
			}
		}
	}
}

// close closes an IMAP client
func (c *client) close() {
	c.conn.Close()
}

// logError sends a log message to the default Logger
func (c *client) logError(err error) {
	log.Printf("IMAP client %s, %v", c.id, err)
}

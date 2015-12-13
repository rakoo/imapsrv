package unpeu

import "testing"

// https://stackoverflow.com/questions/6878590/the-maximum-value-for-an-int-type-in-go#6878625
const maxInt = int(^uint(0) >> 1)

func TestInvalidSequenceSet(t *testing.T) {
	expectedFailing := []string{
		":",
		"1:",
		":1",
	}

	for _, input := range expectedFailing {
		if isValid(input) {
			t.Fatalf("Invalid input is considered valid: %q\n", input)
		}
		_, err := toList(input, maxInt)
		if err == nil {
			t.Fatal("Expected failure for", input)
		}
	}
}

func TestValidSequenceSet(t *testing.T) {
	type vector struct {
		input    string
		max      int
		expected []int
	}

	vectors := []vector{
		{"", maxInt, []int{}},
		{"1", maxInt, []int{1}},
		{"4,7", maxInt, []int{4, 7}},
		{"2:6", maxInt, []int{2, 3, 4, 5, 6}},
		{"4:1", maxInt, []int{1, 2, 3, 4}},
		{"1,*", 10, []int{1, 10}},

		{"1:3,5:7", maxInt, []int{1, 2, 3, 5, 6, 7}},
		{"2:*,6:4", 7, []int{2, 3, 4, 5, 6, 7}},
		{"*:4,5:7", 10, []int{4, 5, 6, 7, 8, 9, 10}},
	}

	for _, v := range vectors {
		if !isValid(v.input) {
			t.Fatalf("Valid input is considered invalid: %q\n", v.input)
		}
		actual, err := toList(v.input, v.max)
		if err != nil {
			t.Fatal("Error parsing sequence set:", err)
		}
		if len(actual) != len(v.expected) {
			t.Log("Error in parsing")
			t.Log("Got     ", actual)
			t.Log("Expected", v.expected)
			t.FailNow()
		}
		for i, act := range actual {
			if act != v.expected[i] {
				t.Log("Error in parsing")
				t.Log("Got     ", actual)
				t.Log("Expected", v.expected)
				t.FailNow()
			}
		}
	}
}

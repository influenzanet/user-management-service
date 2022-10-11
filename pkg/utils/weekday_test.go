package utils

import (
	"testing"
)

func asWeight(n ...int) Weight {
	return CreateWeight(n)
}

func testLookup(t *testing.T, w Weight, value int, expect int) {
	i := w.Lookup(value)
	if i != expect {
		t.Errorf("Expecting index %d for value %d, got %d", expect, value, i)
	}
}

func TestWeekdayLookup(t *testing.T) {

	w := asWeight(1, 1, 1, 1, 1)

	t.Run("W1", func(t *testing.T) {
		t.Logf("Weight %v\n", w)
		testLookup(t, w, 0, 0)
		testLookup(t, w, 1, 1)
		testLookup(t, w, 2, 2)
		testLookup(t, w, 3, 3)
		testLookup(t, w, 4, 4)
		testLookup(t, w, 15, 4)
	})

	w = asWeight(1, 0, 1, 0, 1)

	t.Run("W2", func(t *testing.T) {
		t.Logf("Weight %v\n", w)
		testLookup(t, w, 0, 0)
		testLookup(t, w, 1, 2)
		testLookup(t, w, 2, 4)
	})

	w = asWeight(1, 2, 1, 2)

	t.Run("W3", func(t *testing.T) {
		t.Logf("Weight %v\n", w)
		testLookup(t, w, 0, 0)
		testLookup(t, w, 1, 1)
		testLookup(t, w, 2, 1)
		testLookup(t, w, 3, 2)
		testLookup(t, w, 4, 3)
		testLookup(t, w, 5, 3)
	})

	// No assigneable value at the end
	w = asWeight(1, 2, 0, 0)
	t.Run("W4", func(t *testing.T) {
		t.Logf("Weight %v\n", w)
		testLookup(t, w, 0, 0)
		testLookup(t, w, 1, 1)
		testLookup(t, w, 2, 1)
		testLookup(t, w, 3, 1)
		testLookup(t, w, 4, 1)
		testLookup(t, w, 5, 1)
	})

	w = asWeight(0, 0, 2, 3)
	t.Run("W4", func(t *testing.T) {
		t.Logf("Weight %v\n", w)
		testLookup(t, w, 0, 2)
		testLookup(t, w, 1, 2)
		testLookup(t, w, 2, 3)
		testLookup(t, w, 3, 3)
		testLookup(t, w, 4, 3)
		testLookup(t, w, 5, 3)
	})

}

func testParam(t *testing.T, str string, w []int) {
	p, e := ParseWeeklyWeight(str)
	if e != nil {
		t.Errorf("Bad parse : %s", e)
		return
	}

	t.Logf("%v <=> %v", p, w)
	for i, expected := range w {
		v := p[i]
		if v != expected {
			t.Errorf("Expected %d at %d, got %d", expected, i, v)
		}
	}
}

func testBadString(t *testing.T, str string) {
	_, err := ParseWeeklyWeight(str)
	if err == nil {
		t.Error("Not a bad string")
	}
}

func TestWeekdayParsing(t *testing.T) {

	t.Run("Bad String", func(t *testing.T) {
		testBadString(t, "Toto")
		testBadString(t, "Mon=, Sun=2")
		testBadString(t, "=2, Sun=2")
	})

	t.Run("Good strings",
		func(t *testing.T) {
			testParam(t, "Mon=1", []int{0, 1, 0, 0, 0, 0, 0})
			testParam(t, "Mon=2", []int{0, 2, 0, 0, 0, 0, 0})
			testParam(t, "Sun=1", []int{1, 0, 0, 0, 0, 0, 0})
			testParam(t, "Thu=1", []int{0, 0, 0, 0, 1, 0, 0})
			testParam(t, "Wed=3", []int{0, 0, 0, 3, 0, 0, 0})
			testParam(t, "Tue=4", []int{0, 0, 4, 0, 0, 0, 0})
			testParam(t, "Fri=5", []int{0, 0, 0, 0, 0, 5, 0})
			testParam(t, "Sat=6", []int{0, 0, 0, 0, 0, 0, 6})
		})

}

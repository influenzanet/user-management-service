package utils

import (
	"fmt"
	"strconv"
	"strings"
)

type Interval struct {
	min   int
	max   int
	entry int
}

// Weekday assignation strategy
type Weight struct {
	total     int        // Total of cumulated weights
	intervals []Interval // Range of weight for each entry
}

// CreateWeight creates a weighted structure to handle the weighted assignation
func CreateWeight(w []int) Weight {
	var cumulated int = 0
	intervals := make([]Interval, 0)
	for entry, w := range w {
		if w > 0 {
			var min = cumulated
			cumulated = cumulated + w
			i := Interval{entry: entry, min: min, max: cumulated - 1}
			intervals = append(intervals, i)
		}
	}
	return Weight{intervals: intervals, total: cumulated}
}

// Lookup computes the entry
// Value is expected from 0..total-1
func (w *Weight) Lookup(value int) int {
	if value < 0 {
		value = 0
	}
	var last int = 0
	for _, interval := range w.intervals {
		if value >= interval.min && value <= interval.max {
			return interval.entry
		}
		last = interval.entry
	}
	return last
}

var days = map[string]int{
	"mon": 1,
	"tue": 2,
	"wed": 3,
	"thu": 4,
	"fri": 5,
	"sat": 6,
	"sun": 0,
}

func parseTuple(str string) (string, string) {
	r := strings.Split(str, "=")
	if len(r) < 2 {
		return "", ""
	}
	return strings.ToLower(strings.TrimSpace(r[0])), strings.TrimSpace(r[1])
}

// ParseWeeklyWeight parse a string attributing a weight for each day of the week
// Expected format is a comma separated assignation with [DayName]=Weight with DayName = Mon,Tue,Web,Thu,Frid,Sat,Sun (case insensitive)
// Weight is an integer (0 or positive)
func ParseWeeklyWeight(str string) ([]int, error) {
	weights := make([]int, 7)
	wdays := strings.Split(str, ",")
	for idx, d := range wdays {
		name, value := parseTuple(d)
		if name == "" {
			return weights, fmt.Errorf("expecting day name for entry %d", idx)
		}
		dayIndex, ok := days[name]
		if !ok {
			return weights, fmt.Errorf("invalid day name '%s' for entry %d ", name, idx)
		}
		w, err := strconv.Atoi(value)
		if err != nil {
			return weights, fmt.Errorf("invalid day weight value '%s' for entry %d, expecting integer : %s", value, idx, err)
		}
		if w < 0 {
			return weights, fmt.Errorf("invalid day weight for entry %d, expecting positive integer : %s", idx, err)
		}
		weights[dayIndex] = w
	}
	return weights, nil
}

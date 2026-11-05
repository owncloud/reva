package publicshares

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func TestInsertAttempt(t *testing.T) {
	var testData = map[string]struct {
		attemptList    []*attemptData
		attempt        *attemptData
		expectedResult []*attemptData
	}{
		"general": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
			},
			attempt: &attemptData{ID: 22, Timestamp: 2000},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 22, Timestamp: 2000},
			},
		},
		"add first": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
			},
			attempt: &attemptData{ID: 22, Timestamp: 500},
			expectedResult: []*attemptData{
				&attemptData{ID: 22, Timestamp: 500},
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
			},
		},
		"add last": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
			},
			attempt: &attemptData{ID: 22, Timestamp: 5000},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 22, Timestamp: 5000},
			},
		},
		"add middle": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
			attempt: &attemptData{ID: 48, Timestamp: 1500},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 48, Timestamp: 1500},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
		},
		"already added first": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
			attempt: &attemptData{ID: 111, Timestamp: 1000},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
		},
		"already added last": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
			attempt: &attemptData{ID: 101, Timestamp: 2007},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
		},
		"already added middle": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
			attempt: &attemptData{ID: 123, Timestamp: 1002},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
		},
		"same timestamp": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
			attempt: &attemptData{ID: 789, Timestamp: 2002},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 789, Timestamp: 2002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
		},
		"same timestamp2": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
			attempt: &attemptData{ID: 89, Timestamp: 2002},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 89, Timestamp: 2002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
		},
		"same timestamp already added": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 789, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
			attempt: &attemptData{ID: 126, Timestamp: 2002},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 789, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
		},
		"same timestamp already added2": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 789, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
			attempt: &attemptData{ID: 789, Timestamp: 2002},
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 789, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
		},
		"nil": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 789, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
			attempt: nil,
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1000},
				&attemptData{ID: 123, Timestamp: 1002},
				&attemptData{ID: 126, Timestamp: 2002},
				&attemptData{ID: 789, Timestamp: 2002},
				&attemptData{ID: 101, Timestamp: 2007},
			},
		},
		"empty list": {
			attemptList: []*attemptData{},
			attempt:     &attemptData{ID: 789, Timestamp: 2002},
			expectedResult: []*attemptData{
				&attemptData{ID: 789, Timestamp: 2002},
			},
		},
	}

	for name, test := range testData {
		t.Run(name, func(t *testing.T) {
			result := InsertAttempt(test.attemptList, test.attempt)
			areEqual := slices.EqualFunc(result, test.expectedResult, func(a, b *attemptData) bool {
				return a.ID == b.ID && a.Timestamp == b.Timestamp
			})

			if !areEqual {
				jresult, _ := json.Marshal(result)
				jexpected, _ := json.Marshal(test.expectedResult)
				t.Errorf("attempt data wasn't added properly. result = %s , expected result = %s", jresult, jexpected)
			}
		})
	}
}

func TestCleanAttempts(t *testing.T) {
	now := time.Now().Unix()

	var testData = map[string]struct {
		attemptList    []*attemptData
		timeGap        time.Duration
		expectedResult []*attemptData
	}{
		"just added": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: now},
				&attemptData{ID: 123, Timestamp: now},
				&attemptData{ID: 126, Timestamp: now},
			},
			timeGap: 15 * 24 * time.Hour,
			expectedResult: []*attemptData{
				&attemptData{ID: 111, Timestamp: now},
				&attemptData{ID: 123, Timestamp: now},
				&attemptData{ID: 126, Timestamp: now},
			},
		},
		"some obsolete": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 22},
				&attemptData{ID: 123, Timestamp: now},
				&attemptData{ID: 126, Timestamp: now},
			},
			timeGap: 15 * 24 * time.Hour,
			expectedResult: []*attemptData{
				&attemptData{ID: 123, Timestamp: now},
				&attemptData{ID: 126, Timestamp: now},
			},
		},
		"all obsolete": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 22},
				&attemptData{ID: 123, Timestamp: 23},
				&attemptData{ID: 126, Timestamp: 24},
			},
			timeGap:        15 * 24 * time.Hour,
			expectedResult: []*attemptData{},
		},
	}

	for name, test := range testData {
		t.Run(name, func(t *testing.T) {
			result := CleanAttempts(test.attemptList, test.timeGap)
			areEqual := slices.EqualFunc(result, test.expectedResult, func(a, b *attemptData) bool {
				return a.ID == b.ID && a.Timestamp == b.Timestamp
			})

			if !areEqual {
				jresult, _ := json.Marshal(result)
				jexpected, _ := json.Marshal(test.expectedResult)
				t.Errorf("attempt data wasn't cleaned properly. result = %s , expected result = %s", jresult, jexpected)
			}
		})
	}
}

func TestIsSubsetOfAttempts(t *testing.T) {
	var testData = map[string]struct {
		attemptList    []*attemptData
		subset         []*attemptData
		expectedResult bool
	}{
		"same list": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			expectedResult: true,
		},
		"subset longer": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
				&attemptData{ID: 526, Timestamp: 2234},
			},
			expectedResult: false,
		},
		"new added last": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
				&attemptData{ID: 226, Timestamp: 2234},
			},
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			expectedResult: true,
		},
		"new added middle": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 226, Timestamp: 1034},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			expectedResult: true,
		},
		"new added first": {
			attemptList: []*attemptData{
				&attemptData{ID: 226, Timestamp: 500},
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			expectedResult: true,
		},
		"new with same timestamp": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 223, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			expectedResult: true,
		},
		"new with same timestamp2": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 223, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 223, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			expectedResult: true,
		},
		"matching timestamp different id": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 993, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			expectedResult: false,
		},
		"empty list": {
			attemptList: []*attemptData{},
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 993, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			expectedResult: false,
		},
		"empty subset": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			subset:         []*attemptData{},
			expectedResult: true,
		},
		"empty list and subset": {
			attemptList:    []*attemptData{},
			subset:         []*attemptData{},
			expectedResult: true,
		},
		"nil list": {
			attemptList: nil,
			subset: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 993, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			expectedResult: false,
		},
		"nil subset": {
			attemptList: []*attemptData{
				&attemptData{ID: 111, Timestamp: 1001},
				&attemptData{ID: 123, Timestamp: 1005},
				&attemptData{ID: 126, Timestamp: 1234},
			},
			subset:         nil,
			expectedResult: true,
		},
		"nil list and subset": {
			attemptList:    nil,
			subset:         nil,
			expectedResult: true,
		},
	}

	for name, test := range testData {
		t.Run(name, func(t *testing.T) {
			result := IsSubsetOfAttempts(test.attemptList, test.subset)

			if result != test.expectedResult {
				jlist, _ := json.Marshal(test.attemptList)
				jsub, _ := json.Marshal(test.subset)
				t.Errorf("b isn't a subset of a. a = %s , b = %s", jlist, jsub)
			}
		})
	}
}

// Package modelsort orders model IDs by family and version, matching hax's picker order.
package modelsort

import "sort"

type tokenKind uint8

const (
	tokenVersion tokenKind = iota
	tokenEnd
	tokenDate
	tokenWord
)

type token struct {
	kind tokenKind
	text string
}

// CompareIDs returns a qsort-style ordering. Version continuations precede a bare ID, which
// precedes dated snapshots and then named variants. Numeric runs compare numerically, newest
// first; model families and words compare alphabetically without regard to case.
func CompareIDs(first, second string) int {
	firstPosition, secondPosition := 0, 0
	for {
		firstToken := nextToken(first, &firstPosition)
		secondToken := nextToken(second, &secondPosition)
		if firstToken.kind != secondToken.kind {
			return int(firstToken.kind) - int(secondToken.kind)
		}
		if firstToken.kind == tokenEnd {
			return compareBytes(first, second)
		}

		var compared int
		if firstToken.kind == tokenWord {
			compared = compareWords(firstToken.text, secondToken.text)
		} else {
			compared = compareDigitsNewestFirst(firstToken.text, secondToken.text)
		}
		if compared != 0 {
			return compared
		}
	}
}

// Sort orders IDs in place using CompareIDs.
func Sort(ids []string) {
	sort.Slice(ids, func(first, second int) bool {
		return CompareIDs(ids[first], ids[second]) < 0
	})
}

func nextToken(value string, position *int) token {
	for *position < len(value) && !isAlphaNumeric(value[*position]) {
		(*position)++
	}
	start := *position
	if start == len(value) {
		return token{kind: tokenEnd}
	}
	if isDigit(value[start]) {
		for *position < len(value) && isDigit(value[*position]) {
			(*position)++
		}
		text := value[start:*position]
		kind := tokenVersion
		if len(text) >= 4 {
			kind = tokenDate
		}
		return token{kind: kind, text: text}
	}
	for *position < len(value) && isAlpha(value[*position]) {
		(*position)++
	}
	return token{kind: tokenWord, text: value[start:*position]}
}

func compareDigitsNewestFirst(first, second string) int {
	first = trimLeadingZeros(first)
	second = trimLeadingZeros(second)
	if len(first) != len(second) {
		if len(first) > len(second) {
			return -1
		}
		return 1
	}
	return -compareBytes(first, second)
}

func trimLeadingZeros(value string) string {
	index := 0
	for index < len(value)-1 && value[index] == '0' {
		index++
	}
	return value[index:]
}

func compareWords(first, second string) int {
	length := min(len(first), len(second))
	for index := 0; index < length; index++ {
		left, right := lowerASCII(first[index]), lowerASCII(second[index])
		if left != right {
			if left < right {
				return -1
			}
			return 1
		}
	}
	if len(first) < len(second) {
		return -1
	}
	if len(first) > len(second) {
		return 1
	}
	return 0
}

func compareBytes(first, second string) int {
	length := min(len(first), len(second))
	for index := 0; index < length; index++ {
		if first[index] < second[index] {
			return -1
		}
		if first[index] > second[index] {
			return 1
		}
	}
	if len(first) < len(second) {
		return -1
	}
	if len(first) > len(second) {
		return 1
	}
	return 0
}

func isAlphaNumeric(value byte) bool { return isAlpha(value) || isDigit(value) }
func isAlpha(value byte) bool        { return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' }
func isDigit(value byte) bool        { return value >= '0' && value <= '9' }

func lowerASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

package util

import (
	"fmt"
)

// 按excel格式 R1C1格式转A1
func index2Alphabet(number int) string {
	var letters []byte
	for number > 0 {
		number-- // Excel column digits start at 1 rather than 0.
		letters = append(letters, 'A'+byte(number%26))
		number /= 26
	}
	for left, right := 0, len(letters)-1; left < right; left, right = left+1, right-1 {
		letters[left], letters[right] = letters[right], letters[left]
	}
	return string(letters)
}

// r,c都是base1
func R1C1ToA1(r, c int) string {
	return fmt.Sprintf("%s%d", index2Alphabet(c), r)
}

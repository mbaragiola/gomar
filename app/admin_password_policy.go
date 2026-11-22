package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The pinned Django 5.2.18 list is used as shipped. See admin-password-data/README.md.
//
//go:embed admin-password-data/common-passwords.txt.gz
var commonPasswordData []byte

var commonAdminPasswords = sync.OnceValue(func() map[string]struct{} {
	data, err := gzip.NewReader(bytes.NewReader(commonPasswordData))
	if err != nil {
		panic("invalid embedded common password list")
	}
	defer data.Close()
	list := make(map[string]struct{}, 19_640)
	rows := bufio.NewScanner(data)
	for rows.Scan() {
		list[trimPythonSpace(rows.Text())] = struct{}{}
	}
	if rows.Err() != nil {
		panic("unreadable embedded common password list")
	}
	return list
})

func trimPythonSpace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
	})
}

func validateNewAdminPassword(password, username string) []string {
	var problems []string
	lower := strings.ToLower(password)
	if similarAdminUsername(lower, strings.ToLower(username)) {
		problems = append(problems, similarPassword)
	}
	if utf8.RuneCountInString(password) < 8 {
		problems = append(problems, shortPassword)
	}
	if _, found := commonAdminPasswords()[trimPythonSpace(lower)]; found {
		problems = append(problems, commonPassword)
	}
	if password != "" && strings.IndexFunc(password, func(r rune) bool { return !pythonPasswordDigit(r) }) == -1 {
		problems = append(problems, numericPassword)
	}
	return problems
}

func similarAdminUsername(password, username string) bool {
	if username == "" {
		return false
	}
	// Django compares the entire username and each component split on
	// non-word characters. Go stores no first/last name or email here.
	parts := strings.FieldsFunc(username, func(r rune) bool {
		return r != '_' && !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	parts = append(parts, username)
	passwordLen := utf8.RuneCountInString(password)
	counts := make(map[rune]int)
	for _, r := range password {
		counts[r]++
	}
	for _, part := range parts {
		// SequenceMatcher.quick_ratio() counts the multiset intersection.
		partCounts := make(map[rune]int)
		var matches, partLen int
		for _, r := range part {
			partLen++
			partCounts[r]++
			if partCounts[r] <= counts[r] {
				matches++
			}
		}
		if 20*matches >= 7*(passwordLen+partLen) {
			return true
		}
	}
	return false
}

// CPython 3.14, whose Unicode database is 16.0.0, recognizes both
// Numeric_Type=Decimal and Numeric_Type=Digit for str.isdigit(). Generate
// these inclusive runs by grouping consecutive code points where
// chr(i).isdigit() is true; Go's unicode.IsDigit only covers Decimal.
func pythonPasswordDigit(r rune) bool {
	if '0' <= r && r <= '9' {
		return true
	}
	for _, run := range pythonPasswordDigitRuns {
		if r < run[0] {
			return false
		}
		if r <= run[1] {
			return true
		}
	}
	return false
}

var pythonPasswordDigitRuns = [...][2]rune{
	{0xb2, 0xb3}, {0xb9, 0xb9}, {0x660, 0x669}, {0x6f0, 0x6f9},
	{0x7c0, 0x7c9}, {0x966, 0x96f}, {0x9e6, 0x9ef}, {0xa66, 0xa6f},
	{0xae6, 0xaef}, {0xb66, 0xb6f}, {0xbe6, 0xbef}, {0xc66, 0xc6f},
	{0xce6, 0xcef}, {0xd66, 0xd6f}, {0xde6, 0xdef}, {0xe50, 0xe59},
	{0xed0, 0xed9}, {0xf20, 0xf29}, {0x1040, 0x1049}, {0x1090, 0x1099},
	{0x1369, 0x1371}, {0x17e0, 0x17e9}, {0x1810, 0x1819}, {0x1946, 0x194f},
	{0x19d0, 0x19da}, {0x1a80, 0x1a89}, {0x1a90, 0x1a99}, {0x1b50, 0x1b59},
	{0x1bb0, 0x1bb9}, {0x1c40, 0x1c49}, {0x1c50, 0x1c59}, {0x2070, 0x2070},
	{0x2074, 0x2079}, {0x2080, 0x2089}, {0x2460, 0x2468}, {0x2474, 0x247c},
	{0x2488, 0x2490}, {0x24ea, 0x24ea}, {0x24f5, 0x24fd}, {0x24ff, 0x24ff},
	{0x2776, 0x277e}, {0x2780, 0x2788}, {0x278a, 0x2792}, {0xa620, 0xa629},
	{0xa8d0, 0xa8d9}, {0xa900, 0xa909}, {0xa9d0, 0xa9d9}, {0xa9f0, 0xa9f9},
	{0xaa50, 0xaa59}, {0xabf0, 0xabf9}, {0xff10, 0xff19}, {0x104a0, 0x104a9},
	{0x10a40, 0x10a43}, {0x10d30, 0x10d39}, {0x10d40, 0x10d49}, {0x10e60, 0x10e68},
	{0x11052, 0x1105a}, {0x11066, 0x1106f}, {0x110f0, 0x110f9}, {0x11136, 0x1113f},
	{0x111d0, 0x111d9}, {0x112f0, 0x112f9}, {0x11450, 0x11459}, {0x114d0, 0x114d9},
	{0x11650, 0x11659}, {0x116c0, 0x116c9}, {0x116d0, 0x116e3}, {0x11730, 0x11739},
	{0x118e0, 0x118e9}, {0x11950, 0x11959}, {0x11bf0, 0x11bf9}, {0x11c50, 0x11c59},
	{0x11d50, 0x11d59}, {0x11da0, 0x11da9}, {0x11f50, 0x11f59}, {0x16130, 0x16139},
	{0x16a60, 0x16a69}, {0x16ac0, 0x16ac9}, {0x16b50, 0x16b59}, {0x16d70, 0x16d79},
	{0x1ccf0, 0x1ccf9}, {0x1d7ce, 0x1d7ff}, {0x1e140, 0x1e149}, {0x1e2f0, 0x1e2f9},
	{0x1e4f0, 0x1e4f9}, {0x1e5f1, 0x1e5fa}, {0x1e950, 0x1e959}, {0x1f100, 0x1f10a},
	{0x1fbf0, 0x1fbf9},
}

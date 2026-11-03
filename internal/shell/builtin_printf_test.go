package shell

import "testing"

// Expected results were checked against fish 4.8.1 and bash; where they
// differ, comments say which one is followed.
func TestPrintf(t *testing.T) {
	checkBuiltinCases(t, runScript, []builtinCase{
		// Uses from real configs.
		{`printf '%s\n' a b c`, "a\nb\nc\n", "", 0},
		{`printf "%5.2f|%-4s|%03d\n" 3.14159 ab 7`, " 3.14|ab  |007\n", "", 0},

		// The format is reused until the arguments run out; missing
		// arguments are empty or zero.
		{`printf 'hello\n'`, "hello\n", "", 0},
		{`printf 'x\n' a b`, "x\n", "", 0},
		{`printf '%d %d\n' 1 2 3`, "1 2\n3 0\n", "", 0},
		{`printf '%s %s %s\n' a b`, "a b \n", "", 0},
		{`printf '%s=%s;' a 1 b 2 c`, "a=1;b=2;c=;", "", 0},
		{`printf '%%\n'`, "%\n", "", 0},

		// Strings and characters.
		{`printf '%5s|%-5s|\n' ab cd`, "   ab|cd   |\n", "", 0},
		{`printf '%.2s|%-6.3s|\n' abcdef abcdef`, "ab|abc   |\n", "", 0},
		{`printf '%c%c%c\n' hello … o`, "h…o\n", "", 0},
		{`printf '%3c|%-3c|\n' x y`, "  x|y  |\n", "", 0},
		{`printf '[%c]' ''`, "[]", "", 0}, // bash; fish prints a NUL byte
		// Widths count terminal columns, as in fish.
		{`printf '|%5s|\n' 日本`, "| 日本|\n", "", 0},
		{`printf '|%.3s|\n' 日本語`, "|日|\n", "", 0},
		{`printf '|%3s|\n' é`, "|  é|\n", "", 0},
		{`printf '|%4s|\n' 🇺🇳`, "|  🇺🇳|\n", "", 0},

		// Integers.
		{`printf '%d %i\n' 42 -7`, "42 -7\n", "", 0},
		{`printf '%x %X %o %u\n' 10 11 8 -1`, "a B 10 18446744073709551615\n", "", 0},
		{`printf '%#x %#o %#X %#o\n' 255 8 255 0`, "0xff 010 0XFF 0\n", "", 0},
		{`printf '%+d % d %05d %-5d|\n' 5 5 42 42`, "+5  5 00042 42   |\n", "", 0},
		{`printf '%5.3d|%.0d|%-05d|\n' 7 0 7`, "  007||7    |\n", "", 0},
		{`printf '%*d|%-*d|\n' 5 42 4 7`, "   42|7   |\n", "", 0},
		{`printf '%*d|\n' -5 42`, "42   |\n", "", 0},
		{`printf '%.*d|\n' -3 42`, "42|\n", "", 0},
		{`printf '%d %d %d %d\n' 0x1F 010 -0x10 ' 12'`, "31 8 -16 12\n", "", 0},
		{`printf '%d %d %x\n' "'a" '"é' "'é"`, "97 233 e9\n", "", 0},
		{`printf '%d %d\n' -9223372036854775808 ''`, "-9223372036854775808 0\n", "", 0},
		{`printf '%ld %hd %lld %zd\n' 1 2 3 4`, "1 2 3 4\n", "", 0},

		// Floats.
		{`printf '%f %e %E\n' 1.5 5 6`, "1.500000 5.000000e+00 6.000000E+00\n", "", 0},
		{`printf '%.*f %.0f %.0f %.0f\n' 2 3.14159 0.5 1.5 2.5`, "3.14 0 2 2\n", "", 0},
		{`printf '%g %g %g %g %g\n' 100000 1000000 0.0001 0.00001 -0`, "100000 1e+06 0.0001 1e-05 -0\n", "", 0},
		{`printf '%#g %G %.3g %#.0f\n' 1 1e-10 3.14159 3`, "1.00000 1E-10 3.14 3.\n", "", 0},
		{`printf '%a %A %.0a %.2a\n' 1 255.5 1.5 1`, "0x1p+0 0X1.FFP+7 0x2p+0 0x1.00p+0\n", "", 0},
		{`printf '%010a|%-10a|\n' -1 0`, "-0x0001p+0|0x0p+0    |\n", "", 0},
		{`printf '%f %F %f %5.1f|\n' inf nan -inf infinity`, "inf NAN -inf   inf|\n", "", 0},
		{`printf '%f %f %f\n' 0x1p3 .5 "'A"`, "8.000000 0.500000 65.000000\n", "", 0},
		{`printf '%+.3e|%10.4f|%-10.2e|\n' 12345.678 3.14159265 31415.9265`, "+1.235e+04|    3.1416|3.14e+04  |\n", "", 0},
		{`printf '%.2f %.20f\n' 2.675 0.1`, "2.67 0.10000000000000000555\n", "", 0},

		// Escapes in the format.
		{`printf 'a\tb\\c\n'`, "a\tb\\c\n", "", 0},
		{`printf '\101\x42\u00e9\U0001F600\n'`, "ABé😀\n", "", 0},
		{`printf '\0101|\08|\400'`, "\b1|\x008|\x00", "", 0},
		{`printf '\a\b\f\r\v\"'`, "\a\b\f\r\v\"", "", 0},
		{`printf '\e[1m\E[0m'`, "\x1b[1m\x1b[0m", "", 0}, // \E is bash's
		{`printf 'it\'"'"'s\?'`, "it's?", "", 0},         // bash; fish keeps the backslashes
		{`printf '\q\z\'`, `\q\z\`, "", 0},
		{`printf 'a\cb'; printf 'c\n'`, "ac\n", "", 0},

		// %b expands escapes in the argument.
		{`printf '%b\n' 'a\tb' '\0101\101\0'`, "a\tb\nAA\x00\n", "", 0},
		{`printf '%b|%s\n' 'x\cy' z`, "x", "", 0},
		{`printf '%b' '\x41\u00e9\e\"'`, "Aé\x1b\"", "", 0},
		{`printf '%b' "\\'"`, `\'`, "", 0},
		{`printf '%5b|%-5b|%.1b|\n' ab cd xyz`, "   ab|cd   |x|\n", "", 0}, // bash; fish rejects these

		// %q quotes for the shell (bash 5; 3.2 didn't quote ~ and used
		// octal escapes for any non-ASCII byte).
		{`printf '%q\n' 'a b' "it's" '' '#x' 'a,b' 'x=~'`, "a\\ b\nit\\'s\n''\n\\#x\na\\,b\nx=\\~\n", "", 0},
		{`printf '%q\n' "$(printf 'a\tb')" é`, "$'a\\tb'\né\n", "", 0},
		{`printf '%q\n' '!$&*()[]{}|;<>?^"\'`, `\!\$\&\*\(\)\[\]\{\}\|\;\<\>\?\^\"\\` + "\n", "", 0},
		{`printf '%-6q|\n' 'a b'`, "a\\ b  |\n", "", 0},
		{`s=$(printf %q 'x $y "z"'); eval "v=$s"; echo "$v"`, "x $y \"z\"\n", "", 0},

		// -v assigns instead of printing (bash).
		{`printf -v x '%s-' a b; echo "[$x]"`, "[a-b-]\n", "", 0},
		{`printf -v y -- '%03d' 7; echo "$y"`, "007\n", "", 0},
		{`printf -v e ''; echo "[$e]"`, "[]\n", "", 0},
		{`arr=(a b c); printf -v 'arr[1]' '%s!' hi; echo "${arr[@]}"`, "a hi! c\n", "", 0},
		{`printf -v 1x foo`, "", "printf: 1x: invalid variable name\n", 2},
		{`printf -v`, "", "printf: -v: option requires an argument\nprintf: usage: printf [-v var] format [arguments]\n", 2},
		{`printf -v x`, "", "printf: usage: printf [-v var] format [arguments]\n", 2},

		// -- ends options; anything else is the format, as in fish.
		{`printf -- '-%s\n' z`, "-z\n", "", 0},
		{`printf -- -- x`, "--", "", 0},
		{`printf -a; printf --help`, "-a--help", "", 0},
		{`printf; echo $?`, "2\n", "", 0},

		// A bad number is reported and printed as 0 or as the part that
		// converted; printing continues and the status is 1.
		{`printf '%d|%s\n' abc def`, "0|def\n", "printf: abc: expected a numeric value\n", 1},
		{`printf '%d\n' 12abc`, "12\n", "printf: 12abc: value not completely converted (can't convert 'abc')\n", 1},
		{`printf '%d\n' 08`, "0\n",
			"printf: 08: value not completely converted (can't convert '8')\n" +
				"printf: Hint: a leading '0' without an 'x' indicates an octal number\n", 1},
		{`printf '%f|%d\n' 1e '- 5'`, "1.000000|0\n",
			"printf: 1e: value not completely converted (can't convert 'e')\nprintf: - 5: expected a numeric value\n", 1},
		{`printf '%d\n' 99999999999999999999`, "9223372036854775807\n", "printf: 99999999999999999999: Number out of range\n", 1},
		{`printf '%x\n' 18446744073709551616`, "ffffffffffffffff\n", "printf: 18446744073709551616: Number out of range\n", 1},
		{`printf '%.*s|\n' abc xyz`, "|\n", "printf: abc: expected a numeric value\n", 1},
		{`printf '%d\n' ''`, "0\n", "", 0},

		// Errors that stop all output.
		{`printf '%k\n' 5`, "", "printf: %k: invalid conversion specification\n", 1},
		{`printf '%z\n' 5`, "", "printf: %z\\: invalid conversion specification\n", 1}, // z is a length modifier
		{`printf 'abc%'`, "abc", "printf: %: invalid conversion specification\n", 1},
		{`printf '%5%|'`, "", "printf: %5%: invalid conversion specification\n", 1},
		{`printf '%05s' ab`, "", "printf: %05s: invalid conversion specification\n", 1},
		{`printf '%#d' 5`, "", "printf: %#d: invalid conversion specification\n", 1},
		{`printf '%.3c' a`, "", "printf: %.3c: invalid conversion specification\n", 1},
		{`printf '%lb' x`, "", "printf: %lb: invalid conversion specification\n", 1},
		{`printf 'a\x'`, "a", "printf: missing hexadecimal number in escape\n", 1},
		{`printf '\uzz'`, "", "printf: Missing hexadecimal number in Unicode escape\n", 1},
		{`printf '\U110000'`, "", "printf: Not a valid Unicode character: \\U00110000\n", 1},
		{`printf '%*s\n' 2147483648 abc`, "", "printf: invalid field width: 2147483648\n", 1},
		{`printf '%.*f\n' 2147483648 1`, "", "printf: invalid precision: 2147483648\n", 1},
	})
}

func TestPrintfFish(t *testing.T) {
	checkBuiltinCases(t, runFish, []builtinCase{
		{`printf '%s\n' a b c`, "a\nb\nc\n", "", 0},
		{`printf "%5.2f|%-4s|%03d\n" 3.14159 ab 7`, " 3.14|ab  |007\n", "", 0},
		{`set -l fmt '%-6s|'; printf $fmt x y; echo`, "x     |y     |\n", "", 0},
		{`set -l empty; printf $empty; echo $status`, "2\n", "", 0},
		{`printf '%s=%s\n' (printf '%s\n' a 1 b 2)`, "a=1\nb=2\n", "", 0},
		{`set x (printf '%x' 255); echo $x`, "ff\n", "", 0},
	})
}

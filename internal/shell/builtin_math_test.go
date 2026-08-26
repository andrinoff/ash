package shell

import (
	"strings"
	"testing"
)

type builtinCase struct {
	src    string
	out    string
	err    string
	status int
}

func checkBuiltinCases(t *testing.T, run func(*testing.T, string) (string, string, int), cases []builtinCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			out, errOut, st := run(t, tc.src)
			if out != tc.out || errOut != tc.err || st != tc.status {
				t.Errorf("%s\n got: out=%q err=%q status=%d\nwant: out=%q err=%q status=%d",
					tc.src, out, errOut, st, tc.out, tc.err, tc.status)
			}
		})
	}
}

// Expected results were checked against fish 4.8.1.
func TestMath(t *testing.T) {
	checkBuiltinCases(t, runScript, []builtinCase{
		// Uses from real configs.
		{"i=5; math $i + 1", "6\n", "", 0},
		{"i=5; math $i - 1", "4\n", "", 0},
		{"math 10 / 3", "3.333333\n", "", 0},
		{"math 2 x 3", "6\n", "", 0},
		{"math 'max(3, 7)'", "7\n", "", 0},

		// Operators and precedence.
		{"math 1 + 1", "2\n", "", 0},
		{"math 5 x 3 x 2", "30\n", "", 0},
		{"math '2*3'", "6\n", "", 0},
		{"math 2x 3", "6\n", "", 0},
		{`math $'2 x\t3'`, "6\n", "", 0},
		{"math '(1 + 2) x 3'", "9\n", "", 0},
		{"math '2^3^2'", "512\n", "", 0},
		{"math '-2^2'", "4\n", "", 0},
		{"math 2 ^ -2 ^ 2", "16\n", "", 0},
		{"math 1 - - 1", "2\n", "", 0},
		{"math -5 + 3", "-2\n", "", 0},
		{"math -- -10 % 3", "-1\n", "", 0},
		{"math 10.5 % 3", "1.5\n", "", 0},
		{"math 1 / 3 x 3", "1\n", "", 0},
		{"math 0.1 + 0.2", "0.3\n", "", 0},
		{"math 5 / 2", "2.5\n", "", 0},

		// Numbers.
		{"math 0x10", "16\n", "", 0},
		{"math 0XFF", "255\n", "", 0},
		{"math 0xff.8", "255.5\n", "", 0},
		{"math 0x1p3", "8\n", "", 0},
		{"math 1e3", "1000\n", "", 0},
		{"math 1.5e-3", "0.0015\n", "", 0},
		{"math 1E+2", "100\n", "", 0},
		{"math .5", "0.5\n", "", 0},
		{"math 5.", "5\n", "", 0},
		{"math 1_000 + 1", "1001\n", "", 0},
		{"math 0x_ff", "255\n", "", 0},
		// 0b and 0o are extensions: fish reports an unknown function.
		{"math 0b101", "5\n", "", 0},
		{"math 0o17 + 0B1_1", "18\n", "", 0},

		// Functions and constants.
		{"math pi", "3.141593\n", "", 0},
		{"math e", "2.718282\n", "", 0},
		{"math tau", "6.283185\n", "", 0},
		{"math 'pi()'", "3.141593\n", "", 0},
		{"math 'sin(pi)'", "0\n", "", 0},
		{"math sin pi", "0\n", "", 0},
		{"math 'cos(0)'", "1\n", "", 0},
		{"math 'abs(-5)'", "5\n", "", 0},
		{"math 'fac(5)'", "120\n", "", 0},
		{"math fac 3.7", "6\n", "", 0},
		{"math 'ncr(49, 6)'", "13983816\n", "", 0},
		{"math 'npr(10, 3)'", "720\n", "", 0},
		{"math 'log(100)'", "2\n", "", 0},
		{"math 'log10(1000)'", "3\n", "", 0},
		{"math 'ln(e)'", "1\n", "", 0},
		{"math 'log2(8)'", "3\n", "", 0},
		{"math 'exp(1)'", "2.718282\n", "", 0},
		{"math 'sqrt(2)'", "1.414214\n", "", 0},
		{"math 'round(2.5)' x 'round(-2.5)'", "-9\n", "", 0},
		{"math 'floor(-2.5)'", "-3\n", "", 0},
		{"math 'ceil(-2.5)'", "-2\n", "", 0},
		{"math 'atan2(1, 1)'", "0.785398\n", "", 0},
		{"math atan2 1,2", "0.463648\n", "", 0},
		{"math 'bitand(12, 10)'", "8\n", "", 0},
		{"math 'bitor(12, 10)'", "14\n", "", 0},
		{"math 'bitxor(0xFF, 0x0F)'", "240\n", "", 0},
		{"math 'bitand(-1, 3)'", "0\n", "", 0},
		{"math 'pow(2, 10)'", "1024\n", "", 0},
		{"math pow 2, 3", "8\n", "", 0},
		{"math 'min(3, 1, 2)'", "1\n", "", 0},
		{"math 'max(3)'", "3\n", "", 0},
		{"math 'min(0, -0)'", "-0\n", "", 0},
		{"math 'max(1, 2) + 1'", "3\n", "", 0},

		// Output formatting.
		{"math 2 / 3", "0.666667\n", "", 0},
		{"math -- -0", "-0\n", "", 0},
		{"math 0 x -1", "-0\n", "", 0},
		{"math 0.0000001", "0\n", "", 0},
		{"math 1e15 + 0.3", "1000000000000000.25\n", "", 0},
		{"math 2^52 + 0.5", "4503599627370496\n", "", 0},
		{"math 2^53 - 1", "9007199254740991\n", "", 0},
		{"math 9.9999995", "9.999999\n", "", 0},
		{"math -s 1 0.25", "0.2\n", "", 0},
		{"math -s 1 -0.05", "-0.1\n", "", 0},
		{"math -s0 10 / 3", "3\n", "", 0},
		{"math -s 0 -- -10 / 3", "-3\n", "", 0},
		{"math -s0 '22 / 5 - 5'", "-0\n", "", 0},
		{"math -s3 2 / 3", "0.667\n", "", 0},
		{"math --scale=3 2/3", "0.667\n", "", 0},
		{"math --scale 3 2/3", "0.667\n", "", 0},
		{"math -s max 10/3", "3.333333333333333\n", "", 0},
		{"math -s max pi", "3.141592653589793\n", "", 0},
		// Correctly rounded like the C library: Go's math.Pow is an ulp off.
		{"math -s max 1.1^17", "5.054470284992945\n", "", 0},
		{"math -s max e^tau", "535.491655524764383\n", "", 0},
		{"math -s max 13^0.3", "2.158653844421579\n", "", 0},
		{"math -s max 'exp(10)'", "22026.465794806717895\n", "", 0},
		{"math -m round -s 0 2.5", "3\n", "", 0},
		{"math -m round -s1 -0.25", "-0.3\n", "", 0},
		{"math -m floor -s0 -2.5", "-3\n", "", 0},
		{"math -m ceiling -s0 2.1", "3\n", "", 0},
		{"math -m ceil -s 1 -0.01", "-0\n", "", 0},
		{"math -m trunc -s1 -2.77", "-2.7\n", "", 0},
		{"math -m floor 2 / 3", "0.666666\n", "", 0},
		{"math --scale-mode=round 2 / 3", "0.666667\n", "", 0},
		{"math -b 16 255", "0xff\n", "", 0},
		{"math -b hex 255.9", "0xff\n", "", 0},
		{"math --base=16 31", "0x1f\n", "", 0},
		{"math -b16 0", "0x0\n", "", 0},
		{"math -b 16 -- -255", "-0xff\n", "", 0},
		{"math -b 8 8", "010\n", "", 0},
		{"math -b octal 0", "0\n", "", 0},
		{"math -b 8 -- -9", "-011\n", "", 0},
		{"math -b 16 -s0 255", "0xff\n", "", 0},

		// Errors: a message with a caret under the problem, status 1.
		{"math 10 / 0", "", "math: Error: Division by zero\n'10 / 0'\n    ^\n", 1},
		{"math 10 % 0", "", "math: Error: Division by zero\n'10 % 0'\n    ^\n", 1},
		{"math 5x3", "", "math: Error: Unknown function\n'5x3'\n  ^^\n", 1},
		{"math 'foo(1)'", "", "math: Error: Unknown function\n'foo(1)'\n ^~^\n", 1},
		{"math 2 x", "", "math: Error: Unknown function\n'2 x'\n   ^\n", 1},
		{"math 2 3", "", "math: Error: Missing operator\n'2 3'\n  ^\n", 1},
		{"math 3pi", "", "math: Error: Missing operator\n'3pi'\n  ^\n", 1},
		{"math '1 $ 1'", "", "math: Error: Missing operator\n'1 $ 1'\n   ^\n", 1},
		{"math '(1'", "", "math: Error: Missing closing parenthesis\n'(1'\n   ^\n", 1},
		{"math '1)'", "", "math: Error: Too many arguments\n'1)'\n   ^\n", 1},
		{"math 1 +", "", "math: Error: Too few arguments\n'1 +'\n    ^\n", 1},
		{"math 'max()'", "", "math: Error: Too few arguments\n'max()'\n      ^\n", 1},
		{"math 'sin(1,2)'", "", "math: Error: Too many arguments\n'sin(1,2)'\n       ^^\n", 1},
		{"math 'atan2(1)'", "", "math: Error: Too few arguments\n'atan2(1)'\n         ^\n", 1},
		{"math '()'", "", "math: Error: Unexpected token\n'()'\n   ^\n", 1},
		{"math .", "", "math: Error: Unexpected token\n'.'\n ^\n", 1},
		{"math 1 == 1", "", "math: Error: Logical operations are not supported, use `test` instead\n'1 == 1'\n   ^\n", 1},
		{"math 1e999", "", "math: Error: Number is too large\n'1e999'\n ^\n", 1},
		{"math 'sqrt(-1)'", "", "math: Error: Result is not a number\n'sqrt(-1)'\n", 1},
		{"math 2^1024", "", "math: Error: Result is infinite\n'2^1024'\n", 1},
		{"math 10^16", "", "math: Error: Result magnitude is too large\n'10^16'\n", 1},
		{"math 'fac(20)'", "", "math: Error: Result magnitude is too large\n'fac(20)'\n", 1},
		{"math", "", "math: expected >= 1 arguments; got 0\n", 1},
		{"math '' ''", "", "math: expected >= 1 arguments; got 0\n", 1},

		// Option errors: status 2.
		{"math -s", "", "math: -s: option requires an argument\n", 2},
		{"math --scale", "", "math: --scale: option requires an argument\n", 2},
		{"math -s 16 1", "", "math: 16: invalid scale\n", 2},
		{"math -s x 1", "", "math: x: invalid scale\n", 2},
		{"math -b 2 5", "", "math: 2: invalid base value\n", 2},
		{"math -m bogus 1", "", "math: bogus: invalid mode\n", 2},
		{"math -b 16 -s 2 255", "", "math: invalid option combination, non-zero scale value only valid for base 10\n", 2},
		{"math --help=3", "", "math: --help=3: option does not take an argument\n", 2},
		// As in fish, -sin(1) is -s with the value "in(1)".
		{"math '-sin(1)'", "", "math: in(1): invalid scale\n", 2},
		{"math -- '-sin(1)'", "-0.841471\n", "", 0},
		// Words that aren't options start the expression.
		{"math -pi", "-3.141593\n", "", 0},
		{"math --foo", "", "math: Error: Unknown function\n'--foo'\n   ^~^\n", 1},

		// Input from a pipe.
		{"echo '2 x 21' | math", "42\n", "", 0},
		{"printf '1 +\\n2\\n' | math -s0", "3\n", "", 0},
	})
}

func TestMathStdin(t *testing.T) {
	out, errOut, st := runScriptIn(t, "math", "1 + 2\n")
	if out != "3\n" || errOut != "" || st != 0 {
		t.Fatalf("got %q %q %d", out, errOut, st)
	}
}

func TestMathHelp(t *testing.T) {
	out, errOut, st := runScript(t, "math --help")
	if !strings.HasPrefix(out, "Usage: math") || errOut != "" || st != 0 {
		t.Fatalf("got %q %q %d", out, errOut, st)
	}
}

func TestMathFish(t *testing.T) {
	checkBuiltinCases(t, runFish, []builtinCase{
		{"set i 5; math $i + 1", "6\n", "", 0},
		{"math (math 2 + 3) x 2", "10\n", "", 0},
		{`math "max(3, 7)"`, "7\n", "", 0},
		{"set n 3; while test $n -gt 0; echo $n; set n (math $n - 1); end", "3\n2\n1\n", "", 0},
		{"math 10 / 0; echo $status", "1\n", "math: Error: Division by zero\n'10 / 0'\n    ^\n", 0},
	})
}

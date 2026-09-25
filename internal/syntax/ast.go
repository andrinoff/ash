// Package syntax parses ash source code.
//
// The grammar is a union of POSIX sh (with the bash bits people actually
// use) and fish. Both styles can live in the same file:
//
//	if test -f go.mod; and type -q go     # fish
//	    go build ./...
//	end
//
//	if [ -f go.mod ] && command -v go >/dev/null; then   # sh
//	    go build ./...
//	fi
//
// Ambiguities are resolved by looking at what follows a construct rather
// than by a global mode switch: `if cond; then` is sh, `if cond; ...; end`
// is fish, `name() {` is sh, `function name; ...; end` is fish.
package syntax

// Positions are byte offsets into the source.

type File struct {
	Stmts []*Stmt
}

// Stmt is one entry in a statement list.
type Stmt struct {
	Pos, End   int
	Cmd        Command
	Background bool     // cmd &
	Disown     bool     // cmd &!
	Cond       CondKind // fish: "and cmd" / "or cmd"
}

type CondKind uint8

const (
	CondNone CondKind = iota
	CondAnd
	CondOr
)

type Command interface{ command() }

// BinaryCmd is `x && y` or `x || y`.
type BinaryCmd struct {
	Op   BinOp
	X, Y Command
}

type BinOp uint8

const (
	AndOp BinOp = iota + 1
	OrOp
)

// Pipeline is `a | b | c`, possibly negated and/or timed.
type Pipeline struct {
	Pos     int
	Negate  bool
	Time    bool
	Cmds    []Command
	PipeErr []bool // PipeErr[i]: stderr of Cmds[i] goes down the pipe too (|& or &|)
	ErrOnly []bool // fish "2>|": only stderr goes down the pipe
}

// EnvCmd is fish's `VAR=value <compound command>`.
type EnvCmd struct {
	Assigns []*Assign
	Cmd     Command
}

// CallExpr is a simple command: assignments, words and redirections.
type CallExpr struct {
	Pos, End int
	Assigns  []*Assign
	Args     []*Word
	Redirs   []*Redirect
	// DeclArgs is set for local, declare, typeset, readonly and export:
	// DeclArgs[i] is Args[i] parsed as an assignment, as in local x=~/a or
	// declare -a arr=(1 2), or nil. Such arguments expand the way
	// assignments do, without word splitting or globbing.
	DeclArgs []*Assign
}

type Assign struct {
	Pos     int
	Name    string
	Index   *Word // name[index]=value
	Append  bool  // name+=value
	Value   *Word
	Array   []*Word // name=(a b c)
	IsArray bool
}

type Redirect struct {
	Pos     int
	Fd      int // -1 means the operator's default
	Op      RedirOp
	Target  *Word
	Heredoc *Word // body of << and <<-
}

type RedirOp uint8

const (
	RedirIn          RedirOp = iota + 1 // <
	RedirOut                            // >
	RedirAppend                         // >>
	RedirClobber                        // >|
	RedirNoClobber                      // >? (fish)
	RedirInOut                          // <>
	RedirDupIn                          // <&
	RedirDupOut                         // >&
	RedirAll                            // &>
	RedirAllAppend                      // &>>
	RedirHeredoc                        // <<
	RedirHeredocTabs                    // <<-
	RedirHerestring                     // <<<
)

func (op RedirOp) String() string {
	switch op {
	case RedirIn:
		return "<"
	case RedirOut:
		return ">"
	case RedirAppend:
		return ">>"
	case RedirClobber:
		return ">|"
	case RedirNoClobber:
		return ">?"
	case RedirInOut:
		return "<>"
	case RedirDupIn:
		return "<&"
	case RedirDupOut:
		return ">&"
	case RedirAll:
		return "&>"
	case RedirAllAppend:
		return "&>>"
	case RedirHeredoc:
		return "<<"
	case RedirHeredocTabs:
		return "<<-"
	case RedirHerestring:
		return "<<<"
	}
	return "?"
}

// Block is `{ ...; }` or fish's `begin; ...; end`.
type Block struct {
	Stmts  []*Stmt
	Redirs []*Redirect
	Fish   bool
}

// Subshell is `( ... )`.
type Subshell struct {
	Stmts  []*Stmt
	Redirs []*Redirect
}

type IfClause struct {
	Cond   []*Stmt
	Then   []*Stmt
	Elifs  []*ElifBranch
	Else   []*Stmt
	Redirs []*Redirect
	Fish   bool
}

type ElifBranch struct {
	Cond []*Stmt
	Then []*Stmt
}

type WhileClause struct {
	Fish   bool
	Until  bool
	Cond   []*Stmt
	Body   []*Stmt
	Redirs []*Redirect
}

// ForClause is `for x in words; ...`. Items == nil && !HasIn means "$@".
type ForClause struct {
	Fish   bool
	Var    string
	Items  []*Word
	HasIn  bool
	Body   []*Stmt
	Redirs []*Redirect
}

// CForClause is bash's `for ((init; cond; post))`.
type CForClause struct {
	Init, Cond, Post *Word
	Body             []*Stmt
	Redirs           []*Redirect
}

// CaseClause covers both `case ... esac` and fish's `switch ... end`.
type CaseClause struct {
	Word   *Word
	Items  []*CaseItem
	Redirs []*Redirect
	Fish   bool
}

type CaseItem struct {
	Patterns []*Word
	Body     []*Stmt
	Op       CaseOp
}

type CaseOp uint8

const (
	CaseBreak    CaseOp = iota // ;;
	CaseFall                   // ;&
	CaseContinue               // ;;&
)

// FuncDecl defines a function. Fish functions keep their option words
// (-d, -w, --on-variable, ...) which are evaluated at definition time.
type FuncDecl struct {
	Pos, End int
	Name     *Word
	Opts     []*Word
	Body     []*Stmt // fish
	BodyCmd  Command // sh: name() compound-command
	Fish     bool
	Src      string
}

// ArithCmd is `(( expr ))`.
type ArithCmd struct {
	Expr   *Word
	Redirs []*Redirect
}

// TestClause is `[[ expr ]]`.
type TestClause struct {
	Expr   TestExpr
	Redirs []*Redirect
}

type TestExpr interface{ testExpr() }

type TestBinary struct {
	Op   string // && || == = != =~ < > -eq ... -nt -ot -ef
	X, Y TestExpr
}

type TestUnary struct {
	Op string // ! or -f -z ...
	X  TestExpr
}

type TestWord struct {
	W     *Word
	Regex bool // right hand side of =~
}

type TestParen struct{ X TestExpr }

func (*BinaryCmd) command()   {}
func (*Pipeline) command()    {}
func (*CallExpr) command()    {}
func (*Block) command()       {}
func (*Subshell) command()    {}
func (*IfClause) command()    {}
func (*WhileClause) command() {}
func (*ForClause) command()   {}
func (*CForClause) command()  {}
func (*CaseClause) command()  {}
func (*FuncDecl) command()    {}
func (*ArithCmd) command()    {}
func (*TestClause) command()  {}
func (*EnvCmd) command()      {}

func (*TestBinary) testExpr() {}
func (*TestUnary) testExpr()  {}
func (*TestWord) testExpr()   {}
func (*TestParen) testExpr()  {}

// Word is a shell word made of parts.
type Word struct {
	Pos, End int
	Parts    []WordPart
}

type WordPart interface{ wordPart() }

// Lit is unquoted text. Glob characters in it are live.
type Lit struct{ Value string }

// Quoted is text that came from '...', $'...' or a backslash escape.
type Quoted struct{ Value string }

// DblQuoted is "...". Lit parts inside it are quoted.
type DblQuoted struct{ Parts []WordPart }

// ParamExp is $name, ${name...} or fish's $name[index].
type ParamExp struct {
	Name     string
	Short    bool // $name rather than ${name}
	Length   bool // ${#name}
	Indirect bool // ${!name}
	Index    *Index
	Op       ParamOp
	Arg      *Word // operand: default value, pattern, offset...
	Arg2     *Word // replacement or substring length
	HasArg2  bool
}

// Index is either a fish index list ($x[1 2..3]) or a bash subscript.
type Index struct {
	Fish  bool
	Words []*Word // fish: each word expands to N, N..M, ..M, N..
	Expr  *Word   // bash: arithmetic, or the literals "@" / "*"
	All   bool    // bash [@] or [*]
	Star  bool    // bash [*]
}

type ParamOp uint8

const (
	OpNone           ParamOp = iota
	OpDefault                // :-
	OpDefaultUnset           // -
	OpAssign                 // :=
	OpAssignUnset            // =
	OpError                  // :?
	OpErrorUnset             // ?
	OpAlt                    // :+
	OpAltUnset               // +
	OpTrimPrefix             // #
	OpTrimPrefixLong         // ##
	OpTrimSuffix             // %
	OpTrimSuffixLong         // %%
	OpReplace                // /
	OpReplaceAll             // //
	OpReplacePrefix          // /#
	OpReplaceSuffix          // /%
	OpSubstr                 // :off:len
	OpUpperFirst             // ^
	OpUpperAll               // ^^
	OpLowerFirst             // ,
	OpLowerAll               // ,,
	OpTransform              // @Q @U @L @E @A
	OpPrefixNames            // ${!prefix*}
	OpIndices                // ${!arr[@]}
	OpBad                    // ${(f)x} and the like: an error when expanded; Arg is the text
)

// CmdSubst is $(...), `...` or fish's (...).
type CmdSubst struct {
	Stmts []*Stmt
	Fish  bool   // (...) splits on newlines, $(...) on IFS
	Index *Index // fish: (cmd)[1]
}

// ArithExp is $(( ... )).
type ArithExp struct{ Expr *Word }

// ProcSubst is <(...) or >(...).
type ProcSubst struct {
	Out   bool
	Stmts []*Stmt
}

// BraceExp is {a,b,c} or {1..10}.
type BraceExp struct {
	Alts [][]WordPart
	Seq  *BraceSeq
}

type BraceSeq struct{ Start, End, Step string }

// Tilde is ~ or ~user at the start of a word.
type Tilde struct{ User string }

func (*Lit) wordPart()       {}
func (*Quoted) wordPart()    {}
func (*DblQuoted) wordPart() {}
func (*ParamExp) wordPart()  {}
func (*CmdSubst) wordPart()  {}
func (*ArithExp) wordPart()  {}
func (*ProcSubst) wordPart() {}
func (*BraceExp) wordPart()  {}
func (*Tilde) wordPart()     {}

// LitString returns the word's value if it is made only of literal and
// quoted text, i.e. needs no expansion.
func (w *Word) LitString() (string, bool) {
	if w == nil {
		return "", false
	}
	var s string
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *Lit:
			s += p.Value
		case *Quoted:
			s += p.Value
		case *DblQuoted:
			for _, q := range p.Parts {
				l, ok := q.(*Lit)
				if !ok {
					return "", false
				}
				s += l.Value
			}
		default:
			return "", false
		}
	}
	return s, true
}

// IsUnquotedLit reports whether the word is a single unquoted literal.
func (w *Word) IsUnquotedLit() (string, bool) {
	if w == nil || len(w.Parts) != 1 {
		return "", false
	}
	l, ok := w.Parts[0].(*Lit)
	if !ok {
		return "", false
	}
	return l.Value, true
}

// HasQuotes reports whether any part of the word was quoted or escaped.
func (w *Word) HasQuotes() bool {
	for _, p := range w.Parts {
		switch p.(type) {
		case *Quoted, *DblQuoted:
			return true
		}
	}
	return false
}

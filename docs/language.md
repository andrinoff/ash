# The ash language

ash reads two dialects: POSIX sh (with the bash extensions people actually
type) and fish. You don't pick one. Each construct is recognized by its own
shape, so both of these work, even in the same file:

```sh
if [ -f go.mod ] && command -v go >/dev/null; then
    go build ./...
fi

if test -f go.mod; and type -q go
    go build ./...
end
```

The rule of thumb: a construct written in bash syntax behaves like bash, and
one written in fish syntax behaves like fish. Where the two disagree and the
syntax doesn't tell them apart, ash goes with whichever is less surprising
at an interactive prompt. Those cases are listed at the end.

## Variables are lists

Every variable holds a list of strings. A "normal" variable is a list with
one element.

```sh
set fruits apple banana cherry     # fish
fruits=(apple banana cherry)       # bash, same result

echo $fruits[2]                    # banana (fish indexes from 1)
echo ${fruits[1]}                  # banana (bash indexes from 0)
echo $fruits[-1]                   # cherry
echo $fruits[2..3]                 # banana cherry
echo (count $fruits) ${#fruits[@]} # 3 3
```

`$fruits` expands to one argument per element. There is no word splitting
of variable values, so a value with spaces in it stays one argument, quoted
or not:

```sh
file="my notes.txt"
rm $file          # removes "my notes.txt", not "my" and "notes.txt"
```

Inside double quotes a list is joined with spaces: `"$fruits"` is one
argument, `apple banana cherry`. `"$@"` and `"${arr[@]}"` keep their bash
meaning and expand to separate arguments.

An empty list expands to nothing, which is what you want for optional
flags:

```sh
set opts                  # empty
ls $opts ~                # runs: ls ~
```

`x=""` (bash style) also makes `$x` vanish, like it would in bash. If you
really want an empty argument, quote it: `"$x"`. `set x ""` (fish style)
gives a list with one empty element, which, as in fish, expands to one
empty argument.

A list glued to other text multiplies, fish style: `$fruits.jpg` gives
`apple.jpg banana.jpg cherry.jpg`. When the list is empty the text stays
(`foo$unset` is `foo`), which is the bash behavior and the one fewer
people trip over.

### PATH and friends

Variables whose name ends in `PATH` are lists that get joined with `:` when
exported. This makes both styles do the right thing:

```sh
export PATH="$HOME/bin:$PATH"     # bash habit
set -gx PATH $HOME/bin $PATH      # fish habit
add_path ~/go/bin                 # or let ash do it (skips duplicates and missing dirs)
echo $PATH[1]                     # first directory
echo ${PATH%%:*}                  # also the first directory
```

### Scope

- `name=value` and `export name=value` behave like bash: they change the
  variable where it already exists, otherwise they create a global.
  `local name=value` makes it local to the function.
- `set name value` behaves like fish: it changes an existing variable, or
  creates one local to the current function. `-l` limits it to the current
  block, `-g` makes it global, `-U` makes it universal (saved across
  sessions in `~/.config/ash/variables`), `-x` exports it.
- Functions written in bash syntax (`f() { ...; }`) see their caller's
  local variables, as in bash. Functions written in fish syntax
  (`function f; ...; end`) don't, unless you pass `-S`.

## Quoting

`'single quotes'` are literal. `"double quotes"` expand `$var`, `$(cmd)` and
`` `cmd` ``. A backslash outside quotes escapes the next character, and the
usual C escapes work there too: `\n`, `\t`, `\e`, `\x1b`, `→`.
`$'...'` is bash's ANSI-C quoting.

Files ending in `.fish` are read with fish's quoting rules, where `\'` and
`\\` work inside single quotes and backticks are just characters. Everything
else, including what you type at the prompt, uses POSIX quoting. That way
`sed 's/\\/\//g'` copied from a Stack Overflow answer means what it says.

## Command substitution

```sh
echo "today is $(date +%A)"   # one word, trailing newlines removed
for word in $(echo a b c)     # $(...) unquoted splits on whitespace, like bash
for line in (cat list.txt)    # (...) splits on lines, like fish
```

So `(...)` is the safe one for file names with spaces in them. Fish's
`(cmd)[1]` indexing works too.

`<(cmd)` and `>(cmd)` give you a `/dev/fd/N` path, as in bash:

```sh
diff <(sort a.txt) <(sort b.txt)
```

## Globs

`*`, `?`, `[abc]` and `**` (any depth) work everywhere. Hidden files only
match a pattern that starts with a dot. Results are sorted naturally, so
`file2` comes before `file10`.

A pattern that matches nothing is passed through unchanged, the way bash
does it, which keeps URLs like `example.com/?q=1` working without quotes.
The exceptions are `for` loops and `set`, where a pattern with no matches
produces nothing, so `for f in *.log` simply doesn't run when there are no
logs.

Brace expansion: `{a,b,c}`, `{1..10}`, `{01..10..2}`, `{a..e}`.

## Commands, pipes and redirections

All the usual things: `a | b`, `a && b`, `a || b`, `a; b`, `a &`, `! a`,
and fish's `and`, `or` and `not` keywords.

```sh
make 2>&1 | less          # stderr into the pipe
make |& less              # same thing, bash shorthand
make &| less              # same thing, fish spelling
cmd &>log.txt             # stdout and stderr to a file
cmd >>log.txt 2>/dev/null
grep -c x <<< "$text"     # here-string
cat <<EOF                 # here-document (<<- strips leading tabs)
Hello $USER
EOF
```

`>?` (fish) refuses to overwrite an existing file, as does `>` after
`set -o noclobber`.

## Control flow

Both spellings work and can be nested in any combination.

```sh
for i in 1 2 3; do echo $i; done
for i in 1 2 3; echo $i; end
for ((i = 0; i < 3; i++)); do echo $i; done

while read -r line; do echo "$line"; done < file
while read line; echo $line; end < file

case $file in
    *.go) echo go ;;
    *.rs | *.toml) echo rust ;;
esac

switch $file
    case '*.go'
        echo go
    case '*.rs' '*.toml'
        echo rust
end

[[ $name == a* && -d $dir ]] && echo yes
(( count > 3 )) && echo many
```

In a fish `switch`, quoted patterns are still patterns (`'*.go'` matches
any `.go` file name), as in fish. In a sh `case`, quoting makes them
literal, as in sh.

## Functions

```sh
greet() {
    echo "hello, ${1:-stranger}"
}

function greet --description 'say hello'
    echo hello, $argv[1]
end
```

Fish functions understand `--description`, `--argument-names`, `--wraps`,
`--on-variable`, `--on-event` and `--inherit-variable`. Inside any function
`$argv` and `$1`, `$2`, `$@`, `$#` all work.

Put a function in `~/.config/ash/functions/NAME.ash` and it loads the first
time you call it.

Events you can hook with `--on-event`: `ash_prompt` (before each prompt),
`ash_preexec` and `ash_postexec` (around each command line), `ash_cancel`
and `ash_exit`. The `fish_` names work too.

## Aliases and abbreviations

```sh
alias ll='ls -l'          # bash
alias ll 'ls -l'          # fish
abbr -a gco git checkout  # expands as you type
```

An alias is expanded when the command runs, so later aliases can refer to
earlier ones and `alias ls 'ls -G'` doesn't loop. Abbreviations are
expanded in the editor when you press space or enter, so what ends up in
your history is the real command. `!!` and `!$` are expanded the same way.

## Arithmetic

`$(( ))` and `(( ))` do integer math with C operators, as in bash.
`math` does floating point, as in fish:

```sh
echo $(( 2 ** 10 ))       # 1024
math 10 / 3               # 3.333333
math 'round(2.5) + max(1, 4)'
```

## Where ash had to pick a side

- `set -e`, `set -x`, `set -euo pipefail` and `set -- a b` with no variable
  names are shell options and positional arguments (sh). `set -e NAME` and
  `set -x NAME value` are fish's erase and export.
- `$var[1]` is fish indexing, unless the brackets don't look like an index
  (`"$prefix[0-9]"` in a regex stays text).
- `$#` is bash's argument count. `$$` is the shell's PID.
- `$_` is the last argument of the previous command (bash), not the current
  command name (fish).
- Unquoted `\n` outside quotes is a newline (fish), not the letter n (bash).
- An unmatched glob stays literal (bash) instead of being an error (fish).
- Command substitution with `$(...)` is a subshell: `cd` inside it doesn't
  move you. That's true for fish-style `(...)` as well, which is a small
  difference from fish.

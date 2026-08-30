package shell

// Placeholders for builtins implemented in their own files.

func init() {
	for _, n := range []string{"string", "math", "printf", "argparse", "path", "complete"} {
		if _, ok := builtins[n]; !ok {
			name := n
			builtins[n] = func(sh *Shell, io *IO, args []string) int {
				io.Err.WriteString(name + ": not implemented yet\n")
				return 1
			}
		}
	}
}

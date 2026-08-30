package shell

func init() {
	registerIntegration(integration{"starship", "starship", "draw the prompt with starship", setupStarship})
}

// setupStarship hands the prompt to starship; the frontend runs it.
func setupStarship(sh *Shell, io *IO, exe string) string {
	sh.SetPromptProvider("starship")
	// A virtualenv's activate script must not prepend to the prompt.
	sh.SetVar("VIRTUAL_ENV_DISABLE_PROMPT", []string{"1"}, setOpts{scope: setGlobal})
	return "the prompt is drawn by " + exe
}

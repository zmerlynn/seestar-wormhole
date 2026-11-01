package guitest

// UXScenarios are synthetic single-page captures, without native credentials,
// real cloud connections, discovery sockets, or proxy listeners.
const UXScenarios = "computer,incompatible,http-permission,http-conflict,bind-error,discovery-error,saved-password,save-denied,save-timeout,remove-denied,remove-timeout,settings-failure,release-uncertain,browser-copy,details,lost-controls,telescope-picker"

func RequiresRelease(scenario string) bool {
	switch scenario {
	case "relaunch", "dark-relaunch", "saved-relaunch", "errors", "http-permission", "http-conflict", "bind-error", "discovery-error":
		return false
	default:
		return true
	}
}

package app

import (
	"errors"
	"fmt"
	"net"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/hirochachacha/go-smb2/v2/x/protocol"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/remote"
)

const (
	addressError  = "Enter the telescope address, with an optional port."
	folderError   = "Enter the full path of a folder."
	intervalError = "Enter a whole number of minutes from 1 to 1440."
)

// statusBadNetworkName is the NTSTATUS a server answers when it has no
// share by the name asked for.
const statusBadNetworkName = 0xC00000CC

// normalizeAddress checks an address as typed: host or host:port, where a
// bare host gets port 445 and an IPv6 host may be bare or in brackets. It
// returns host:port.
func normalizeAddress(s string) (string, bool) {
	s = strings.TrimSpace(s)
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		host, port = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"), "445"
		if strings.Contains(host, ":") && net.ParseIP(host) == nil {
			return "", false
		}
	}
	if host == "" || strings.ContainsAny(host, " \t/\\[]@?#") {
		return "", false
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return "", false
	}
	return net.JoinHostPort(host, port), true
}

// checkInput checks the form's fields and returns the settings they make,
// or a message for each field in error, keyed by field.
func checkInput(in input) (Settings, map[string]string) {
	errs := map[string]string{}
	set := Settings{Version: settingsVersion, Folder: strings.TrimSpace(in.Folder), SkipBuiltIn: in.SkipBuiltIn, StartCopying: in.StartCopying}
	var ok bool
	if set.Address, ok = normalizeAddress(in.Address); !ok {
		errs["address"] = addressError
	}
	if !filepath.IsAbs(set.Folder) {
		errs["folder"] = folderError
	}
	n, err := strconv.Atoi(strings.TrimSpace(in.Interval))
	if err != nil || n < 1 || n > 1440 {
		errs["interval"] = intervalError
	}
	set.IntervalMinutes = n
	for field, lines := range map[string][]string{"excludes": in.Excludes, "includes": in.Includes} {
		patterns, msg := checkPatterns(lines)
		if msg != "" {
			errs[field] = msg
		}
		if field == "excludes" {
			set.Excludes = patterns
		} else {
			set.Includes = patterns
		}
	}
	return set, errs
}

// checkPatterns checks a pattern box's lines with the CLI's parser. Blank
// lines are skipped but counted, so the line number in a message is the
// one the user sees. It returns the patterns, or a message for the first
// line in error.
func checkPatterns(lines []string) ([]string, string) {
	patterns := []string{}
	for i, line := range lines {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if _, err := engine.ParsePattern(line); err != nil {
			return nil, fmt.Sprintf("Line %d: %s. Correct this pattern and save again.", i+1, err)
		}
		patterns = append(patterns, line)
	}
	return patterns, ""
}

// testMessage words Test's result for the page.
func testMessage(endpoint string, entries []remote.Entry, err error) string {
	host, port, _ := net.SplitHostPort(endpoint)
	var dnsErr *net.DNSError
	switch {
	case err == nil:
		var folders []string
		for _, e := range entries {
			if e.Kind == remote.Dir {
				folders = append(folders, path.Base(e.Path))
			}
		}
		const start = "Connected. The telescope's share has "
		switch n := len(folders); n {
		case 0:
			return start + "no folders."
		case 1:
			return start + "the folder " + folders[0] + "."
		case 2:
			return start + "the folders " + folders[0] + " and " + folders[1] + "."
		case 3:
			return start + folders[0] + ", " + folders[1] + " and 1 more folder."
		default:
			return fmt.Sprintf("%s%s, %s and %d more folders.", start, folders[0], folders[1], n-2)
		}
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "Can't find " + host + ". Try the telescope's IP address."
	case errors.Is(err, syscall.ECONNREFUSED):
		return host + " answered but refused port " + port + ". Is this the telescope's address?"
	case isNoShare(err):
		return `Connected, but this isn't a Seestar: it has no "EMMC Images" share.`
	default:
		return "Can't reach " + endpoint + ". Check that the telescope is on and on the same network as this computer, or that seestar-wormhole is running."
	}
}

// isNoShare reports whether err is a server's answer that it has no such
// share.
func isNoShare(err error) bool {
	rerr, ok := errors.AsType[*protocol.ResponseError](err)
	return ok && rerr.Code == statusBadNetworkName
}

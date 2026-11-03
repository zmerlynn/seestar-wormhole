package main

import (
	"github.com/spf13/pflag"
	"github.com/zmerlynn/seestar-copy/internal/engine"
)

// selectionFlags are the flags that choose which part of the share a
// command works on. Every such command registers them with
// [selectionFlags.register] and checks them with [selectionFlags.parse], so
// they mean the same everywhere.
type selectionFlags struct {
	excludes          []string
	noDefaultExcludes bool
	includes          []string
}

func (f *selectionFlags) register(fs *pflag.FlagSet) {
	fs.StringArrayVar(&f.excludes, "exclude", nil, "skip files and folders that match this pattern, as well as the built-in ones; repeatable")
	fs.BoolVar(&f.noDefaultExcludes, "no-default-excludes", false, "do not skip what the built-in patterns match")
	fs.StringArrayVar(&f.includes, "include", nil, "keep only files that match this pattern or are in a folder that does; excludes still win; repeatable")
}

// parse checks the flags, without touching the network or the disk. The
// built-in patterns are checked as --exclude flags.
func (f selectionFlags) parse() (engine.Selector, error) {
	all := f.excludes
	if !f.noDefaultExcludes {
		all = append(append([]string{}, engine.DefaultExcludes...), f.excludes...)
	}
	var s engine.Selector
	for _, flag := range all {
		p, err := parsePattern("--exclude", flag)
		if err != nil {
			return engine.Selector{}, err
		}
		s.Excludes = append(s.Excludes, p)
	}
	for _, flag := range f.includes {
		p, err := parsePattern("--include", flag)
		if err != nil {
			return engine.Selector{}, err
		}
		s.Includes = append(s.Includes, p)
	}
	return s, nil
}

// parsePattern checks the value of option, a pattern flag, and reports a
// broken rule as a usage error.
func parsePattern(option, flag string) (engine.Pattern, error) {
	p, err := engine.ParsePattern(flag)
	if err != nil {
		return engine.Pattern{}, &usageError{Option: option, Rule: err.Error()}
	}
	return p, nil
}

package main

import "testing"

// The statement a refusal offers to make the missing database is pasted into
// a client, so the name in it is an identifier as MySQL reads one: bare when
// it is plain, in backticks when it is not. Bare, `my-app` is a subtraction
// and `orders 2024` is two words, and the refusal's remedy was a syntax error.
func TestTheCreateDatabaseStatementQuotesWhatNeedsIt(t *testing.T) {
	for name, want := range map[string]string{
		"app":         "CREATE DATABASE app",
		"app_2024":    "CREATE DATABASE app_2024",
		"my-app":      "CREATE DATABASE `my-app`",
		"orders 2024": "CREATE DATABASE `orders 2024`",
		"2024":        "CREATE DATABASE `2024`",
		"odd`name":    "CREATE DATABASE `odd``name`",
	} {
		if got := createDatabase(name); got != want {
			t.Errorf("createDatabase(%q) = %q, want %q", name, got, want)
		}
	}
}

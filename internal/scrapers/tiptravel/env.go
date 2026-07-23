// Package tiptravel env helpers. Separated so tests can override the
// environment source without touching the rest of the package.
package tiptravel

import "os"

func defaultLookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}
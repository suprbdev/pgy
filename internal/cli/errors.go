package cli

import "errors"

func AsExitCode(err error) (int, bool) {
    var e *exitCodeError
    if errors.As(err, &e) { return e.code, true }
    return 0, false
}



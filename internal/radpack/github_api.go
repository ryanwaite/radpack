package radpack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type githubAPI func(ctx context.Context, method, endpoint string, body any) (int, []byte, error)

// Use the caller's selected gh credential. Never fall back to another account
// or print raw API errors that may contain tokens or SSO authorization URLs.
func callGitHub(ctx context.Context, method, endpoint string, body any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"api", "--hostname", "github.com", "--include", "--method", method, endpoint}
	var input []byte
	if body != nil {
		var err error
		input, err = json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		args = append(args, "--input", "-")
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.WaitDelay = time.Second
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	var output limitedBuffer
	cmd.Stdout = &output
	err := cmd.Run()
	code, data, parseErr := parseGitHubResponse(output.buffer.Bytes())
	if parseErr != nil {
		return 0, nil, fmt.Errorf("GitHub request failed or returned an unreadable response; check gh authentication and access (raw output withheld)")
	}
	if err != nil && code >= 200 && code < 300 {
		return 0, nil, fmt.Errorf("GitHub command failed after a response; inspect progress before retrying")
	}
	return code, data, nil
}

func parseGitHubResponse(output []byte) (int, []byte, error) {
	// gh --include prints a status line and headers before the JSON body.
	separator := bytes.Index(output, []byte("\r\n\r\n"))
	size := 4
	if separator < 0 {
		separator = bytes.Index(output, []byte("\n\n"))
		size = 2
	}
	if separator < 0 {
		return 0, nil, fmt.Errorf("missing HTTP headers")
	}
	fields := strings.Fields(strings.SplitN(string(output[:separator]), "\n", 2)[0])
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return 0, nil, fmt.Errorf("missing HTTP status")
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil || status < 100 || status > 599 {
		return 0, nil, fmt.Errorf("invalid HTTP status")
	}
	return status, output[separator+size:], nil
}

func githubFailure(status int) error {
	switch status {
	case 401:
		return fmt.Errorf("GitHub authentication failed; sign in with the intended personal account")
	case 403:
		return fmt.Errorf("GitHub refused access; check repository permissions, token scopes, SAML authorization, and policy. No other credential was tried")
	case 404:
		return fmt.Errorf("GitHub resource is missing or not accessible to the selected credential")
	default:
		return fmt.Errorf("GitHub request failed (HTTP %d); raw response withheld", status)
	}
}

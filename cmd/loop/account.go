// The account commands: loop login, logout and status.
// Each one wraps the matching auth.Client call and prints a one-line result, with login printing
// the sign-in URL in case the browser does not open.

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rzhang57/loop/internal/auth"
)

func login(ctx context.Context, authClient *auth.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	creds, err := authClient.Login(ctx, func(url string) {
		fmt.Println("Opening your browser to sign in. If it doesn't open, visit:")
		fmt.Println(url)
	})
	if err != nil {
		return err
	}
	fmt.Printf("Signed in as %s.\n", creds.Email)
	return nil
}

func logout(ctx context.Context, authClient *auth.Client) error {
	err := authClient.Logout(ctx)
	switch {
	case errors.Is(err, auth.ErrNotSignedIn):
		fmt.Println("Not signed in.")
		return nil
	case err != nil:
		return err
	}
	fmt.Println("Signed out.")
	return nil
}

func status(ctx context.Context, authClient *auth.Client) error {
	creds, err := authClient.Current(ctx)
	if errors.Is(err, auth.ErrNotSignedIn) {
		fmt.Println("Not signed in. Run: loop login")
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := authClient.AccessToken(ctx); errors.Is(err, auth.ErrSessionExpired) {
		fmt.Println("Your session has ended. Run: loop login")
		return nil
	} else if err != nil {
		return err
	}
	fmt.Printf("Signed in as %s.\n", creds.Email)
	return nil
}

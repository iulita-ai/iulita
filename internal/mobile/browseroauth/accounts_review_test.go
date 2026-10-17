package browseroauth

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/iulita-ai/iulita/internal/auth"
	"github.com/iulita-ai/iulita/internal/domain"
)

type failingUserReader struct {
	UserReader
	err error
}

func (r failingUserReader) GetUser(context.Context, string) (*domain.User, error) { return nil, r.err }
func TestPasswordAccountLookupDistinguishesMissingFromUnavailable(t *testing.T) {
	temporary := errors.New("account database temporarily unavailable")
	for _, inputErr := range []error{temporary, sql.ErrNoRows, nil} {
		_, err := (PasswordAccounts{Users: failingUserReader{err: inputErr}}).Lookup(context.Background(), "owner")
		if errors.Is(inputErr, temporary) {
			if !errors.Is(err, temporary) || errors.Is(err, auth.ErrUserNotFound) {
				t.Fatal(err)
			}
		} else if !errors.Is(err, auth.ErrUserNotFound) {
			t.Fatal(err)
		}
	}
}

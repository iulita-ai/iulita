//go:build !linux && !darwin

package oauthstore

type RealmLock struct{}

func LockRealm(string) (*RealmLock, error) { return nil, ErrKeys }
func (*RealmLock) Close() error            { return nil }

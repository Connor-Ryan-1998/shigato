package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost is the cost factor for staff password hashes.
const bcryptCost = 12

// HashPassword hashes password with bcrypt, for staff accounts created by
// the admin create-owner command.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	return string(hash), err
}

// checkPassword reports whether password matches hash.
func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// dummyHash is compared against when the email is unknown, so a login
// attempt against a nonexistent account takes about as long as one against
// a real account with a wrong password.
var dummyHash = must(HashPassword("shigoto-dummy-password-for-constant-time-login"))

func must(hash string, err error) string {
	if err != nil {
		panic(err)
	}
	return hash
}

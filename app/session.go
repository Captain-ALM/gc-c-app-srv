package app

import (
	"bytes"
	"crypto/sha512"
	"golang.local/app-srv/conf"
	"golang.local/gc-c-db/db"
	"golang.local/gc-c-db/tables"
	googleAuthIDTokenVerifier "golang.local/google-auth-id-token-verifier"
)

var jwtVerifier = googleAuthIDTokenVerifier.Verifier{}

func isNilOrEmpty(bts []byte) bool {
	if len(bts) != 64 {
		return true
	}
	return bytes.Equal(bts, make([]byte, 64))
}

func NewMasterSession() *Session {
	return &Session{}
}

func NewSession(jwtToken string, hashToken []byte, manager *db.Manager, cnf conf.AppYaml) *Session {
	if jwtToken != "" {
		cSet, err := jwtVerifier.ClaimIDToken(jwtToken, cnf.OAuthAudiences)
		if err == nil && cSet.Email != "" {
			tHash := sha512.Sum512([]byte(jwtToken))
			sesMeta := tables.User{Email: cSet.Email, TokenHash: tHash[:]}
			err = manager.Save(&sesMeta)
			if err == nil {
				return &Session{metadata: sesMeta}
			} else {
				DebugPrintln(err.Error())
			}
		}
		if err != nil {
			DebugPrintln(err.Error())
		}
	}
	if isNilOrEmpty(hashToken) {
		return nil
	} else {
		sesMeta := tables.User{TokenHash: hashToken}
		err := manager.Load(&sesMeta)
		if err == nil {
			return &Session{metadata: sesMeta}
		}
		DebugPrintln(err.Error())
		return nil
	}
}

type Session struct {
	metadata tables.User
}

func (s *Session) ValidateJWT(jwtToken string, manager *db.Manager, cnf conf.AppYaml) bool {
	if s == nil {
		return false
	}
	cSet, err := jwtVerifier.ClaimIDToken(jwtToken, cnf.OAuthAudiences)
	if err == nil && cSet.Email == s.metadata.Email {
		tHash := sha512.Sum512([]byte(jwtToken))
		s.metadata.TokenHash = tHash[:]
		err = manager.Save(&s.metadata)
		return DebugErrIsNil(err)
	}
	if err != nil {
		DebugPrintln(err.Error())
	}
	return false
}

func (s *Session) ValidateHash(hashToken []byte, manager *db.Manager) bool {
	if s == nil {
		return false
	}
	err := manager.Load(&s.metadata)
	if err == nil {
		return bytes.Equal(hashToken, s.metadata.TokenHash)
	}
	DebugPrintln(err.Error())
	return false
}

func (s *Session) Invalidate(manager *db.Manager) bool {
	if s == nil {
		return false
	}
	s.metadata.TokenHash = nil
	err := manager.Save(&s.metadata)
	return DebugErrIsNil(err)
}

func (s *Session) DeleteUser(manager *db.Manager) bool {
	if s == nil {
		return false
	}
	err := manager.Delete(s.metadata.GetIDObject())
	return DebugErrIsNil(err)
}

func (s *Session) IsMasterServer() bool {
	if s == nil {
		return false
	}
	return s.metadata.Email == "" && isNilOrEmpty(s.metadata.TokenHash)
}

func (s *Session) GetEmail() string {
	if s == nil {
		return ""
	}
	return s.metadata.Email
}

func (s *Session) GetTokenHash() []byte {
	if s == nil {
		return nil
	}
	return s.metadata.TokenHash
}

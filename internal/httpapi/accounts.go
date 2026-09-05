package httpapi

import (
	"net/http"

	"github.com/abdeen-labs/hark/internal/auth"
)

type accountResponse struct {
	User userDTO `json:"user"`
}

type accountsResponse struct {
	Users []userDTO `json:"users"`
}

type provisionAccountRequest struct {
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func (s *server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	users, err := s.opts.Auth.ListAccounts(r.Context(), auth.PrincipalFrom(r.Context()))
	if err != nil {
		s.writeAuthError(w, r, "listing accounts failed", err)
		return
	}
	out := accountsResponse{Users: make([]userDTO, 0, len(users))}
	for _, user := range users {
		out.Users = append(out.Users, newUserDTO(user))
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, r, http.StatusOK, out)
}

func (s *server) handleProvisionAccount(w http.ResponseWriter, r *http.Request) {
	var body provisionAccountRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	user, err := s.opts.Auth.ProvisionAccount(r.Context(), auth.PrincipalFrom(r.Context()), auth.CreateAccountParams{
		Username: body.Username, Password: body.Password,
		DisplayName: body.DisplayName, Email: body.Email,
	})
	if err != nil {
		s.writeAuthError(w, r, "provisioning an account failed", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, r, http.StatusCreated, accountResponse{User: newUserDTO(*user)})
}

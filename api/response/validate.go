package response

import (
	"net/http"

	"github.com/go-playground/validator/v10"
)

var validate = validator.New()

func Validate(w http.ResponseWriter, s interface{}) bool {
	if err := validate.Struct(s); err != nil {
		msg := err.Error()
		if ve, ok := err.(validator.ValidationErrors); ok && len(ve) > 0 {
			msg = ve[0].Field() + " is " + ve[0].Tag()
			if ve[0].Param() != "" {
				msg += " " + ve[0].Param()
			}
		}
		Error(w, msg, http.StatusBadRequest)
		return false
	}
	return true
}

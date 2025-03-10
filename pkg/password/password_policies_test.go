package password

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPolicies_Validate(t *testing.T) {
	type fields struct {
		minCharacters          int
		minLowerCaseCharacters int
		minUpperCaseCharacters int
		minDigits              int
		minSpecialCharacters   int
		bannedPasswordsList    map[string]struct{}
	}
	tests := []struct {
		name    string
		fields  fields
		args    string
		wantErr bool
		errMsg  string
	}{
		{
			name: "all in one",
			fields: fields{
				minCharacters:          100,
				minLowerCaseCharacters: 29,
				minUpperCaseCharacters: 29,
				minDigits:              10,
				minSpecialCharacters:   32,
			},
			args: "1234567890abcdefghijklmnopqrstuvwxyzäöüABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÜ !\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~",
		},
		{
			name: "exactly",
			fields: fields{
				minCharacters:          19,
				minLowerCaseCharacters: 7,
				minUpperCaseCharacters: 7,
				minDigits:              1,
				minSpecialCharacters:   1,
			},
			args: "0äÖ-世界іІїЇЯяЙйßAaBb",
		},
		{
			name: "error",
			fields: fields{
				minCharacters:          2,
				minLowerCaseCharacters: 2,
				minUpperCaseCharacters: 2,
				minDigits:              2,
				minSpecialCharacters:   2,
			},
			args:    "0äÖ-",
			wantErr: true,
			errMsg:  "at least 2 lowercase letters are required\nat least 2 uppercase letters are required\nat least 2 numbers are required\nat least 2 special characters are required  !\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~",
		},
		{
			name: "banned list",
			fields: fields{
				minCharacters:          10,
				minLowerCaseCharacters: 3,
				minUpperCaseCharacters: 3,
				minDigits:              3,
				minSpecialCharacters:   1,
				bannedPasswordsList:    map[string]struct{}{"123abcABC!": struct{}{}},
			},
			args:    "123abcABC!",
			wantErr: true,
			errMsg:  "unfortunately, your password is commonly used. please pick a harder-to-guess password for your safety",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewPasswordPolicy(
				tt.fields.minCharacters,
				tt.fields.minLowerCaseCharacters,
				tt.fields.minUpperCaseCharacters,
				tt.fields.minDigits,
				tt.fields.minSpecialCharacters,
				tt.fields.bannedPasswordsList,
			)
			err := s.Validate(tt.args)
			if tt.wantErr {
				assert.EqualError(t, err, tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestPasswordPolicies_Count(t *testing.T) {
	type want struct {
		wantCharacters          int
		wantLowerCaseCharacters int
		wantUpperCaseCharacters int
		wantDigits              int
		wantSpecialCharacters   int
	}
	tests := []struct {
		name    string
		fields  want
		args    string
		wantErr bool
	}{
		{
			name: "all in one",
			fields: want{
				wantCharacters:          101,
				wantLowerCaseCharacters: 29,
				wantUpperCaseCharacters: 29,
				wantDigits:              10,
				wantSpecialCharacters:   33,
			},
			args: "1234567890abcdefghijklmnopqrstuvwxyzäöüABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÜ !\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~",
		},
		{
			name: "length only",
			fields: want{
				wantCharacters:          3,
				wantLowerCaseCharacters: 0,
				wantUpperCaseCharacters: 0,
				wantDigits:              0,
				wantSpecialCharacters:   0,
			},
			args: "世界ß",
		},
		{
			name: "length only",
			fields: want{
				wantCharacters:          21,
				wantLowerCaseCharacters: 7,
				wantUpperCaseCharacters: 7,
				wantDigits:              1,
				wantSpecialCharacters:   3,
			},
			args: "0äÖ-世界 іІїЇЯяЙй ßAaBb",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i := NewPasswordPolicy(
				tt.fields.wantCharacters,
				tt.fields.wantLowerCaseCharacters,
				tt.fields.wantUpperCaseCharacters,
				tt.fields.wantDigits,
				tt.fields.wantSpecialCharacters,
				nil,
			)
			s := i.(*Policies)
			if got := s.count(tt.args); got != tt.fields.wantCharacters {
				t.Errorf("count() = %v, want %v", got, tt.fields.wantCharacters)
			}
			if got := s.countLowerCaseCharacters(tt.args); got != tt.fields.wantLowerCaseCharacters {
				t.Errorf("countLowerCaseCharacters() = %v, want %v", got, tt.fields.wantLowerCaseCharacters)
			}
			if got := s.countUpperCaseCharacters(tt.args); got != tt.fields.wantUpperCaseCharacters {
				t.Errorf("countUpperCaseCharacters() = %v, want %v", got, tt.fields.wantUpperCaseCharacters)
			}
			if got := s.countDigits(tt.args); got != tt.fields.wantDigits {
				t.Errorf("countDigits() = %v, want %v", got, tt.fields.wantDigits)
			}
			if got := s.countSpecialCharacters(tt.args); got != tt.fields.wantSpecialCharacters {
				t.Errorf("countSpecialCharacters() = %v, want %v", got, tt.fields.wantSpecialCharacters)
			}
		})
	}
}

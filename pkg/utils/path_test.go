package utils

import (
	"reflect"
	"testing"
)

func TestEncodePath(t *testing.T) {
	t.Log(EncodePath("http://localhost:5244/d/123#.png"))
}

func TestFixAndCleanPath(t *testing.T) {
	datas := map[string]string{
		"":                          "/",
		".././":                     "/",
		"../../.../":                "/...",
		"x//\\y/":                   "/x/y",
		".././.x/.y/.//..x../..y..": "/.x/.y/..x../..y..",
	}
	for key, value := range datas {
		if FixAndCleanPath(key) != value {
			t.Logf("raw %s fix fail", key)
		}
	}
}

func TestGetPathHierarchy(t *testing.T) {
	testCases := map[string][]string{
		"":                                    {"/"},
		"/":                                   {"/"},
		"/home":                               {"/", "/home"},
		"/home/user":                          {"/", "/home", "/home/user"},
		"/home/user/documents":                {"/", "/home", "/home/user", "/home/user/documents"},
		"/home/user/documents/files/test.txt": {"/", "/home", "/home/user", "/home/user/documents", "/home/user/documents/files", "/home/user/documents/files/test.txt"},
		"home":                                {"/", "/home"},
		"home/user":                           {"/", "/home", "/home/user"},
		"./home/":                             {"/", "/home"},
		"..//home//user/../././":              {"/", "/home"},
		"/home///user///documents///":         {"/", "/home", "/home/user", "/home/user/documents"},
		"/home/user with spaces/doc":          {"/", "/home", "/home/user with spaces", "/home/user with spaces/doc"},
		"/home/user@domain.com/files":         {"/", "/home", "/home/user@domain.com", "/home/user@domain.com/files"},
		"/home/.hidden/.config":               {"/", "/home", "/home/.hidden", "/home/.hidden/.config"},
	}

	for input, expected := range testCases {
		t.Run(input, func(t *testing.T) {
			result := GetPathHierarchy(input)
			if !reflect.DeepEqual(result, expected) {
				t.Errorf("GetPathHierarchy(%q) = %v, want %v", input, result, expected)
			}
		})
	}
}

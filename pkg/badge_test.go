package pkg

import "testing"

func TestExtractLinkedIn(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]string
		want  string
	}{
		{
			name:  "full https URL",
			input: map[string]string{"LinkedIn": "https://www.linkedin.com/in/ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "full URL with trailing slash",
			input: map[string]string{"LinkedIn": "https://www.linkedin.com/in/ab-nour/"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "without www",
			input: map[string]string{"LinkedIn": "https://linkedin.com/in/ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "no protocol with www",
			input: map[string]string{"LinkedIn": "www.linkedin.com/in/ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "no protocol no www",
			input: map[string]string{"LinkedIn": "linkedin.com/in/ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "path only: in/handle",
			input: map[string]string{"LinkedIn": "in/ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "bare handle",
			input: map[string]string{"LinkedIn": "ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "http URL",
			input: map[string]string{"LinkedIn": "http://www.linkedin.com/in/ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "empty value",
			input: map[string]string{"LinkedIn": ""},
			want:  "",
		},
		{
			name:  "none value",
			input: map[string]string{"LinkedIn": "None"},
			want:  "",
		},
		{
			name:  "no linkedin key",
			input: map[string]string{"Twitter": "ab-nour"},
			want:  "",
		},
		{
			name:  "case insensitive key",
			input: map[string]string{"Your LinkedIn Profile": "ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "whitespace around value",
			input: map[string]string{"LinkedIn": "  ab-nour  "},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "path with leading slash: /in/handle",
			input: map[string]string{"LinkedIn": "/in/ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
		{
			name:  "leading slash bare handle",
			input: map[string]string{"LinkedIn": "/ab-nour"},
			want:  "https://www.linkedin.com/in/ab-nour",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractLinkedIn(tt.input)
			if got != tt.want {
				t.Errorf("ExtractLinkedIn(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

package mounter

import "testing"

func TestFindMountOption(t *testing.T) {
	type args struct {
		options []string
		target  string
	}
	tests := []struct {
		name  string
		args  args
		want  string
		want1 bool
	}{
		{
			"with value",
			args{
				[]string{"nolock,proto=tcp,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2,noresvport,vers=3"},
				"vers",
			},
			"3", true,
		},
		{
			"without value",
			args{
				[]string{"nolock,proto=tcp,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2,noresvport,tls,vers=3"},
				"tls",
			},
			"", true,
		},
		{
			"not found",
			args{
				[]string{"nolock,proto=tcp,rsize=1048576,wsize=1048576,hard,timeo=600,retrans=2,noresvport,vers=3"},
				"tls",
			},
			"", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1 := FindMountOption(tt.args.options, tt.args.target)
			if got != tt.want {
				t.Errorf("FindMountOption() got = %v, want %v", got, tt.want)
			}
			if got1 != tt.want1 {
				t.Errorf("FindMountOption() got1 = %v, want %v", got1, tt.want1)
			}
		})
	}
}

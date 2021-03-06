package rewrite

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/pmezard/go-difflib/difflib"
)

func testRewritePackage(t *testing.T, c *Config, expect string) {
	testRewritePackageWithInput(t, c, "", expect)
}

func testRewritePackageWithInput(t *testing.T, c *Config, input, expect string) {
	const dirname = "tmp"
	err := os.MkdirAll(dirname, 0777)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dirname)

	if input != "" {
		err = copyDir(dirname, input)
		if err != nil {
			t.Fatal(err)
		}
	}

	os.Setenv("GOPACKAGE", "GOPACKAGE")

	err = os.Chdir(dirname)
	if err != nil {
		t.Fatal(err)
	}

	err = c.RewritePackage()
	os.Chdir("..")
	if err != nil {
		t.Fatal(err)
	}

	assertEqualDir(t, expect, dirname)
}

func copyDir(to, from string) error {
	fi, err := ioutil.ReadDir(from)
	if err != nil {
		return err
	}
	for _, info := range fi {
		if info.IsDir() {
			continue
		}

		tof, err := os.Create(filepath.Join(to, info.Name()))
		if err != nil {
			return err
		}
		defer tof.Close()

		fromf, err := os.Open(filepath.Join(from, info.Name()))
		if err != nil {
			return err
		}
		defer fromf.Close()

		_, err = io.Copy(tof, fromf)
		if err != nil {
			return err
		}
	}
	return nil
}

func assertEqualDir(t *testing.T, path1, path2 string) {
	fi1, err := ioutil.ReadDir(path1)
	if err != nil {
		t.Fatal(err)
	}
	fi2, err := ioutil.ReadDir(path2)
	if err != nil {
		t.Fatal(err)
	}
	var fn1, fn2 []string
	for _, info := range fi1 {
		fn1 = append(fn1, fmt.Sprintf("%s\n", info.Name()))
	}
	for _, info := range fi2 {
		fn2 = append(fn2, fmt.Sprintf("%s\n", info.Name()))
	}

	diff := difflib.UnifiedDiff{
		A:        fn1,
		B:        fn2,
		FromFile: fmt.Sprintf("Expect %s", path1),
		ToFile:   fmt.Sprintf("Got %s", path2),
		Context:  3,
	}
	text, _ := difflib.GetUnifiedDiffString(diff)
	if text != "" {
		t.Fatalf("DIR\n%s", text)
	}

	for _, info := range fi1 {
		p1 := filepath.Join(path1, info.Name())
		p2 := filepath.Join(path2, info.Name())
		if info.IsDir() {
			assertEqualDir(t, p1, p2)
		} else {
			b1, err := ioutil.ReadFile(p1)
			if err != nil {
				t.Fatal(err)
			}
			b2, err := ioutil.ReadFile(p2)
			if err != nil {
				t.Fatal(err)
			}
			diff := difflib.UnifiedDiff{
				A:        difflib.SplitLines(string(b1)),
				B:        difflib.SplitLines(string(b2)),
				FromFile: fmt.Sprintf("Expect %s", p1),
				ToFile:   fmt.Sprintf("Got %s", p2),
				Context:  3,
			}
			text, _ := difflib.GetUnifiedDiffString(diff)
			if text != "" {
				t.Fatalf("FILE\n%s", text)
			}
		}
	}
}

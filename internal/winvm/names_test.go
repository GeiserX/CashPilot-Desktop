package winvm

import "testing"

func TestNames(t *testing.T) {
	if err := ProductNames().Validate(); err != nil {
		t.Errorf("product names: %v", err)
	}
	for i := 1; i <= MaxLabInstance; i++ {
		n, err := LabNames(i)
		if err != nil {
			t.Fatal(err)
		}
		if err := n.Validate(); err != nil {
			t.Errorf("lab %d: %v", i, err)
		}
		if n.VM == ProductNames().VM || n.DataDir == ProductNames().DataDir || n.GuardTask == ProductNames().GuardTask {
			t.Errorf("lab %d shares an object with the product: %+v", i, n)
		}
	}
	for _, i := range []int{0, -1, 10} {
		if _, err := LabNames(i); err == nil {
			t.Errorf("LabNames(%d) accepted", i)
		}
	}
	bad := ProductNames()
	bad.GuardTask = `\Microsoft\Windows\Defrag`
	if bad.Validate() == nil {
		t.Errorf("a foreign guard task path was accepted")
	}
	bad = ProductNames()
	bad.VM = "CashPilot VM; x"
	if bad.Validate() == nil {
		t.Errorf("a name with a space and a semicolon was accepted")
	}
}

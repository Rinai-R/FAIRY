package runtime

import "testing"

func TestDomainsDescribeRuntimeAsOnlyCompositionRoot(t *testing.T) {
	domains := Domains()
	compositionRoots := 0
	seen := make(map[string]bool, len(domains))
	for _, domain := range domains {
		if domain.Name == "" || domain.Owns == "" {
			t.Fatalf("incomplete domain metadata: %#v", domain)
		}
		if seen[domain.Name] {
			t.Fatalf("duplicate domain %q", domain.Name)
		}
		seen[domain.Name] = true
		if domain.Composition {
			compositionRoots++
			if domain.Name != "runtime" {
				t.Fatalf("composition root = %q, want runtime", domain.Name)
			}
		}
	}
	if compositionRoots != 1 {
		t.Fatalf("composition roots = %d, want 1", compositionRoots)
	}

	domains[0].DependsOn[0] = "mutated"
	if Domains()[0].DependsOn[0] == "mutated" {
		t.Fatal("Domains returned mutable shared metadata")
	}
}

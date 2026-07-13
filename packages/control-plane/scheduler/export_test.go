package scheduler

// export_test.go — expõe helpers internos ao pacote de teste externo
// (scheduler_test) apenas para asserção. Não faz parte da API pública.

// CompareSemVerForTest expõe compareSemVer para os testes.
func CompareSemVerForTest(a, b string) int { return compareSemVer(a, b) }

// ValidSemVerForTest expõe a validação de SemVer para os testes.
func ValidSemVerForTest(s string) bool {
	_, ok := parseSemVer(s)
	return ok
}

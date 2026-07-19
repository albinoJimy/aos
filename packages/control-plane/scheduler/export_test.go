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

// DeriveDesiredReplicasForTest expõe a fórmula pura deriveDesiredReplicas (AOS-107)
// para asserção directa das suas propriedades (crescimento, monotonia, limite por
// headroom, 0-crescimento fail-closed).
func DeriveDesiredReplicasForTest(queueDepth int, p95WaitNanos int64, headroom HeadroomSnapshot, cfg ReplicaScalerConfig) int {
	return deriveDesiredReplicas(queueDepth, p95WaitNanos, headroom, cfg)
}

// DeriveMaxSpawnForTest expõe deriveMaxSpawn (AOS-028) para os testes de escala
// verificarem que max_spawn acompanha o headroom.
func DeriveMaxSpawnForTest(headroomTokens, headroomRequests, costTokens int64) int {
	return deriveMaxSpawn(headroomTokens, headroomRequests, costTokens)
}

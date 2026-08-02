package planapproval

// Verdict é o VEREDICTO humano sobre o plano proposto (AC2): aprovar tal-e-qual, editar
// (podar/reordenar/anotar) ou rejeitar. É ORDENADO do mais restritivo ao menos: o
// valor-zero é [VerdictReject] — FAIL-CLOSED: uma decisão indeterminada REJEITA o plano,
// nunca o liberta para o spawn.
type Verdict uint8

const (
	// VerdictReject — o plano é rejeitado; nenhum spawn é libertado. Valor-zero (fail-closed).
	VerdictReject Verdict = iota
	// VerdictApprove — o plano é aprovado TAL-E-QUAL; o orquestrador pode spawnar.
	VerdictApprove
	// VerdictEdit — o plano é aprovado com REVISÃO (podado/reordenado/anotado): o
	// [PlanDecision] transporta o grafo revisto, que o orquestrador RECONSTRÓI num novo
	// DAG antes de spawnar (não há poda in-place).
	VerdictEdit
)

// String devolve a forma textual canónica do veredicto (selada no span/audit). Um valor
// fora do domínio devolve "reject" (fail-closed).
func (v Verdict) String() string {
	switch v {
	case VerdictApprove:
		return "approve"
	case VerdictEdit:
		return "edit"
	default:
		return "reject"
	}
}

// Approved indica se o veredicto LIBERTA o spawn (approve ou edit) — o complemento de
// rejeitar. É o predicado que o [PlanGate] usa para registar o run como aprovado no
// [SpawnGuard]: tanto aprovar tal-e-qual como aprovar uma revisão autorizam o
// orquestrador a spawnar (a revisão para o MESMO run).
func (v Verdict) Approved() bool { return v == VerdictApprove || v == VerdictEdit }

// PlanDecision é a decisão sobre o plano (AC2). É mais RICA que a
// [risk.ConfirmationResponse] binária: um veredicto de EDIÇÃO transporta o grafo REVISTO
// (RevisedNodes/RevisedEdges) que o orquestrador reconstrói. A decisão binária
// (aprovar/rejeitar) continua a passar pelo [risk.ConfirmationChannel] (o [hitl.Channel])
// para assinatura e não-repúdio; a EDIÇÃO é a camada rica que o canal não modela.
type PlanDecision struct {
	// Verdict é o veredicto humano (approve/edit/reject).
	Verdict Verdict
	// Approver identifica QUEM decidiu (do [risk.ConfirmationResponse.Approver], resolvido
	// a uma chave pinada pelo Channel). Vazio numa auto-aprovação (sem humano) ou numa
	// rejeição sem aprovador verificado. Sem segredos.
	Approver string
	// RevisedNodes é o grafo de nós REVISTO (só em [VerdictEdit]): o plano podado/
	// reordenado/anotado que o orquestrador reconstrói. Nil/ignorado noutros veredictos.
	RevisedNodes []PlanNode
	// RevisedEdges são as arestas do grafo revisto (só em [VerdictEdit]).
	RevisedEdges [][2]string
	// AutoApproved indica que a decisão foi AUTO-APROVADA por nível de autonomia (L4/L5),
	// sem gate humano — consumo de AOS-089, não decisão de nível.
	AutoApproved bool
	// ReviewedNodes lista os task_ids que o humano REVIU item-a-item na triagem. Quando a
	// imposição de revisão forçada está ligada ([WithForcedReview]), o gate exige que TODO
	// nó forçado (Class >= gray ou capability_gap) esteja aqui — aprovar sem rever um nó
	// forçado é recusado fail-closed ([ErrForcedReviewMissing]). A superfície de edição
	// ([PlanReviewer]) preenche-o; vazio numa auto-aprovação.
	ReviewedNodes []string
	// Diff é o DIFF ESTRUTURAL da edição (antes→depois, ao nível dos nós/arestas). Só é
	// preenchido num [VerdictEdit]; nil noutros veredictos. Sem segredos (só topologia).
	Diff *PlanDiff
	// Reason descreve o desfecho (sem segredos).
	Reason string
}

// RevisedPlan projecta a decisão de edição num [Plan] pronto a devolver ao orquestrador,
// preservando o RunID/Agent/Domain do plano original e substituindo nós/arestas pelo
// grafo revisto. Só é significativa quando [PlanDecision.Verdict] == [VerdictEdit]; para
// outros veredictos devolve o plano original inalterado.
func (d PlanDecision) RevisedPlan(original Plan) Plan {
	if d.Verdict != VerdictEdit {
		return original
	}
	return Plan{
		RunID:  original.RunID,
		Agent:  original.Agent,
		Domain: original.Domain,
		Nodes:  d.RevisedNodes,
		Edges:  d.RevisedEdges,
	}
}

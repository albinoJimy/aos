package planvalidate

// Capability é UMA entrada do snapshot de capabilities pinado: uma ferramenta
// conhecida do REG, com a versão e o digest a que está fixada e o seu estado de
// admissibilidade. É DADO PINADO (trusted): faz parte do input imutável do
// validador, não é obtida por lookup vivo.
type Capability struct {
	// Name+Version+Digest são a referência PINADA (tecnica/18 §3.3). Uma [plan.ToolRef]
	// só resolve se casar EXACTAMENTE nos três.
	Name    string
	Version string
	Digest  string
	// Deprecated marca uma capability retirada: presente no snapshot para
	// diagnóstico, mas INADMISSÍVEL para planeamento novo — resolve para rejeição,
	// nunca para trimming silencioso.
	Deprecated bool
	// Admissible é a decisão de política do snapshot (allowlist): se falso, a
	// ferramenta existe mas não é admitida para este planeamento. Fail-closed.
	Admissible bool
}

// Snapshot é o conjunto PINADO de capabilities contra o qual a regra 3 resolve as
// `tools[]` do plano. É passado como ARGUMENTO ao validador — nunca um lookup vivo
// (determinismo: o mesmo snapshot dá sempre o mesmo veredicto).
//
// Hash liga-se ao `capabilities_hash` de [plan.PlannerMeta] (AOS-243): este pacote
// NÃO o computa; apenas confere, na regra 1, que o snapshot recebido é aquele
// contra o qual o plano foi carimbado (binding fail-closed).
type Snapshot struct {
	Hash  string
	Tools []Capability
}

// toolKey é a chave de resolução (nome+versão). O digest é conferido à parte para
// distinguir "versão desconhecida" de "digest não bate" no diagnóstico allowlisted.
type toolKey struct {
	name    string
	version string
}

// index constrói o mapa de resolução do snapshot. Em duplicados (nome+versão
// repetidos no snapshot pinado) o PRIMEIRO vence — determinístico e independente da
// ordem de iteração de mapas. É uma função pura sobre o snapshot recebido.
func (s Snapshot) index() map[toolKey]Capability {
	idx := make(map[toolKey]Capability, len(s.Tools))
	for _, c := range s.Tools {
		k := toolKey{name: c.Name, version: c.Version}
		if _, seen := idx[k]; seen {
			continue // primeiro vence: resolução estável
		}
		idx[k] = c
	}
	return idx
}

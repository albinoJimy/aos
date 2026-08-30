package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// snapshot.go — O SNAPSHOT PINADO DE CAPABILITIES, do qual sai o ORÁCULO DE EFEITO.
//
// # Porque um ficheiro, e o que isso NÃO é
//
// Em produção o snapshot vem do Registry (REG): o conjunto de capabilities PINADO
// (nome+versão+digest) com os eixos de risco que o classificador SA-ROC consome. Aqui
// é carregado de um ficheiro porque este comando não compõe o REG — e isso é uma
// limitação de ESCOPO deste binário, não do mecanismo: o `planvalidate.Snapshot` que
// sai daqui é o MESMO tipo que o validador de admissão usa, e o oráculo que dele se
// deriva é o MESMO `Snapshot.EffectOracle()`. Trocar a fonte é trocar estas 40 linhas.
//
// # Porque eixos por NOME e não pelos inteiros do enum
//
// `risk.Sensitivity`/`Egress`/`Reversibility` são `uint8` sem forma textual de
// desserialização. Serializá-los como inteiros faria de um ficheiro de configuração um
// campo minado: `0` é, nos três eixos, o valor DESCONHECIDO — e desconhecido é
// fail-closed, logo «sensível/externo/irreversível». Um zero por distracção não daria
// um erro: daria uma capability silenciosamente tratada como perigosa, e o operador
// veria um verificador sem autoridade sem perceber porquê.
//
// Por isso os eixos entram por NOME e um nome desconhecido é ERRO, não um default.
// O único default admitido é a AUSÊNCIA do campo, que resolve para o valor
// fail-closed — a mesma direcção que o resto do sistema, mas agora deliberada.

// capabilityJSON é a forma de ficheiro de uma capability pinada.
type capabilityJSON struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Digest     string `json:"digest"`
	Deprecated bool   `json:"deprecated,omitempty"`
	Admissible bool   `json:"admissible"`
	// Eixos de risco por NOME. Ausente ⇒ o valor fail-closed do eixo.
	Sensitivity   string `json:"sensitivity,omitempty"`
	Egress        string `json:"egress,omitempty"`
	Reversibility string `json:"reversibility,omitempty"`
}

// snapshotJSON é a forma de ficheiro do snapshot pinado.
type snapshotJSON struct {
	Hash  string           `json:"hash"`
	Tools []capabilityJSON `json:"tools"`
}

// sensibilidades/egressos/reversibilidades mapeiam nome → enum. O valor fail-closed de
// cada eixo está presente por nome próprio, para que declará-lo seja possível e
// explícito em vez de se obter por omissão.
var (
	sensibilidades = map[string]risk.Sensitivity{
		"unknown":   risk.SensitivityUnknown,
		"public":    risk.SensitivityPublic,
		"internal":  risk.SensitivityInternal,
		"sensitive": risk.SensitivitySensitive,
	}
	egressos = map[string]risk.Egress{
		"unknown":  risk.EgressUnknown,
		"none":     risk.EgressNone,
		"internal": risk.EgressInternal,
		"external": risk.EgressExternal,
	}
	reversibilidades = map[string]risk.Reversibility{
		"unknown":      risk.ReversibilityUnknown,
		"reversible":   risk.Reversible,
		"irreversible": risk.Irreversible,
	}
)

// carregarSnapshot lê e valida o snapshot pinado. Fail-closed em tudo: ficheiro
// ilegível, JSON com campos desconhecidos, eixo de risco por nome desconhecido, ou
// snapshot sem capabilities — nenhum resolve para um default silencioso.
func carregarSnapshot(path string) (planvalidate.Snapshot, error) {
	var vazio planvalidate.Snapshot
	raw, err := os.ReadFile(path)
	if err != nil {
		return vazio, fmt.Errorf("snapshot de capabilities %q: %w", path, err)
	}
	var doc snapshotJSON
	dec := json.NewDecoder(bytes.NewReader(raw))
	// Campos desconhecidos são ERRO: um eixo escrito com o nome errado (`egres`,
	// `reversibility_`) passaria despercebido e a capability resolveria fail-closed —
	// «perigosa» — sem que ninguém soubesse porquê.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return vazio, fmt.Errorf("snapshot de capabilities %q: %w", path, err)
	}
	if len(doc.Tools) == 0 {
		return vazio, fmt.Errorf("snapshot de capabilities %q: sem capabilities — um snapshot vazio faz o oráculo de efeito devolver «efeito» para tudo, que é o default que ele existe para substituir", path)
	}
	snap := planvalidate.Snapshot{Hash: doc.Hash, Tools: make([]planvalidate.Capability, 0, len(doc.Tools))}
	for i, c := range doc.Tools {
		sens, err := resolverEixo("sensitivity", c.Sensitivity, sensibilidades, risk.SensitivityUnknown)
		if err != nil {
			return vazio, fmt.Errorf("capability #%d (%s): %w", i, c.Name, err)
		}
		eg, err := resolverEixo("egress", c.Egress, egressos, risk.EgressUnknown)
		if err != nil {
			return vazio, fmt.Errorf("capability #%d (%s): %w", i, c.Name, err)
		}
		rev, err := resolverEixo("reversibility", c.Reversibility, reversibilidades, risk.ReversibilityUnknown)
		if err != nil {
			return vazio, fmt.Errorf("capability #%d (%s): %w", i, c.Name, err)
		}
		snap.Tools = append(snap.Tools, planvalidate.Capability{
			Name: c.Name, Version: c.Version, Digest: c.Digest,
			Deprecated: c.Deprecated, Admissible: c.Admissible,
			Sensitivity: sens, Egress: eg, Reversibility: rev,
		})
	}
	return snap, nil
}

// resolverEixo traduz o nome de um eixo de risco. Vazio ⇒ o valor fail-closed;
// desconhecido ⇒ ERRO (nunca um default silencioso).
func resolverEixo[T comparable](eixo, nome string, tabela map[string]T, ausente T) (T, error) {
	if nome == "" {
		return ausente, nil
	}
	v, ok := tabela[nome]
	if !ok {
		var zero T
		return zero, fmt.Errorf("eixo %s desconhecido: %q", eixo, nome)
	}
	return v, nil
}

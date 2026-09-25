package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

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
// # O ficheiro é conferido com o nó (AOS-441)
//
// Um ficheiro editado à mão pode divergir das tools que o nó de facto tem — e divergiu: o
// snapshot de produção nomeou `fs.read` durante semanas enquanto o nó lhe chamava `doc_read`.
// Por isso, sempre que há executor de nós (AOS_ORQ_NODE_URL), o snapshot é comparado com o
// catálogo do nó (`GET /tools`) no ARRANQUE do `consume` e do `serve`, e um que diverge é
// RECUSADO com a divergência nomeada — ver [conferirSnapshotComONo]. O ficheiro continua a ser a
// fonte dos eixos de risco; o que deixa de ser é um ficheiro que ninguém compara.
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

// ErrSnapshotDivergeDoNo — o snapshot pinado nomeia tools que o nó não tem, com outro contrato, ou
// com menos risco do que o nó declara (AOS-441). Fail-closed: o `consume` e o `serve` não arrancam.
var ErrSnapshotDivergeDoNo = errors.New("aos-orq: o snapshot de capabilities diverge do catalogo de tools do no")

// ErrCatalogoDoNoIlegivel — o catálogo de tools do nó não se leu (transporte, credencial, nó
// anterior ao AOS-441) ou veio incoerente (nomes repetidos). Distinto de [ErrSnapshotDivergeDoNo]
// porque não houve comparação — mas igualmente fail-closed: sem catálogo não se arranca.
var ErrCatalogoDoNoIlegivel = errors.New("aos-orq: catalogo de tools do no ilegivel")

// snapshotConferido guarda o snapshot que o `serve` conferiu com o nó no arranque. O valor-zero é
// «não houve conferência» (sem executor de nós), e aí lê-se o ficheiro como antes.
type snapshotConferido struct {
	snap planvalidate.Snapshot
	ok   bool
}

// obter devolve o snapshot que o `serve` usa: o JÁ CONFERIDO, quando houve conferência, e só na
// falta dela o lido do ficheiro. Reler o ficheiro depois de o conferir abria uma janela em que o
// que se valida e sela não era o que se conferiu (TOCTOU).
func (c snapshotConferido) obter(path string) (planvalidate.Snapshot, error) {
	if c.ok {
		return c.snap, nil
	}
	return carregarSnapshot(path)
}

// leitorDoCatalogo é o que [conferirSnapshotComONo] precisa do cliente do nó. É uma interface para
// que os testes possam dar um catálogo sem um servidor.
type leitorDoCatalogo interface {
	CatalogoDeTools(ctx context.Context) ([]toolDoNo, error)
}

// conferirSnapshotComONo carrega o snapshot pinado e compara-o com o catálogo de tools do nó.
// Devolve o snapshot SÓ quando bate. Chama-se no arranque, antes de reclamar um pedido (consume)
// e antes de tomar posse de um run (serve): uma divergência é um erro de configuração que se
// repetiria em cada tentativa.
func conferirSnapshotComONo(ctx context.Context, cli leitorDoCatalogo, path string) (planvalidate.Snapshot, error) {
	snap, err := carregarSnapshot(path)
	if err != nil {
		return planvalidate.Snapshot{}, err
	}
	cat, err := cli.CatalogoDeTools(ctx)
	if err != nil {
		// NÃO é «o snapshot diverge»: não se chegou a comparar nada. Dizê-lo mandaria o operador
		// editar um snapshot que pode estar certo, quando o que falhou foi a rede ou a credencial.
		return planvalidate.Snapshot{}, fmt.Errorf("%w: %v — o snapshot %q não foi conferido", ErrCatalogoDoNoIlegivel, err, path)
	}
	if err := compararSnapshotComCatalogo(snap, cat); err != nil {
		return planvalidate.Snapshot{}, fmt.Errorf("snapshot %q: %w", path, err)
	}
	return snap, nil
}

// compararSnapshotComCatalogo diz, tool a tool do snapshot, o que não bate com o nó. Três regras:
//
//  1. a tool EXISTE no nó com o mesmo nome — é o nome que a lista-branca do run compara, e um nome
//     que o nó não tem deixa o nó do plano sem nenhuma tool utilizável (o caso `fs.read`);
//  2. o DIGEST é o do contrato que o nó oferece — a referência pinada é nome+versão+digest
//     (tecnica/18 §3.3) e um digest que o nó não reconhece não pina nada (o `sha256:aaa`);
//  3. os eixos que o nó DECLARA (`egress`, `reversibility`) não são MENOS arriscados no snapshot.
//     O snapshot pode ser mais conservador do que o nó; nunca menos, porque é dele que sai a
//     classe de risco que decide a aprovação automática. A reversibilidade não entra no digest do
//     contrato, e é por isso que se compara à parte.
//
// A VERSÃO não se compara: o manifesto do nó não versiona tools (o registo pina todas em 1.0.0),
// pelo que uma diferença de versão não diria nada sobre a tool. A SENSIBILIDADE também não: o nó
// não a declara, e o snapshot continua a ser a sua única fonte.
//
// Tools do nó que o snapshot não nomeia NÃO são divergência: o planeador simplesmente não as usa.
func compararSnapshotComCatalogo(snap planvalidate.Snapshot, cat []toolDoNo) error {
	doNo := make(map[string]toolDoNo, len(cat))
	nomes := make([]string, 0, len(cat))
	for _, t := range cat {
		// Um nome repetido não tem leitura segura: «o último ganha» escolheria em silêncio um dos
		// dois contratos, e a lista-branca do nó não distingue qual.
		if _, dup := doNo[t.Name]; dup {
			return fmt.Errorf("%w: a tool %q aparece mais de uma vez no catálogo do nó", ErrCatalogoDoNoIlegivel, t.Name)
		}
		doNo[t.Name] = t
		// Com o digest: é o que o operador precisa de copiar para o snapshot, e dá-lo aqui
		// poupa uma segunda recusa só para o descobrir.
		nomes = append(nomes, t.Name+" "+t.Digest)
	}
	sort.Strings(nomes)
	oNoTem := strings.Join(nomes, ", ")
	if oNoTem == "" {
		oNoTem = "nenhuma — o nó não oferece tools ao modelo (AOS_MODEL_ENDPOINT/AOS_MODEL_TOOLS)"
	}

	var divergencias []string
	for _, c := range snap.Tools {
		t, ok := doNo[c.Name]
		if !ok {
			divergencias = append(divergencias, fmt.Sprintf("tool %q não existe no nó (o nó tem: %s)", c.Name, oNoTem))
			continue
		}
		if c.Digest != t.Digest {
			divergencias = append(divergencias, fmt.Sprintf("tool %q: digest do snapshot %q, digest do nó %q", c.Name, c.Digest, t.Digest))
		}
		// `unknown` é um valor legítimo do nó (egress que o manifesto não declara) e conta como o
		// pior caso; o que não se reconhece de todo é divergência.
		if eg, ok := egressos[t.Egress]; !ok {
			divergencias = append(divergencias, fmt.Sprintf("tool %q: o nó declara egress %q, que este aos-orq não reconhece", c.Name, t.Egress))
		} else if rankDeEgress(c.Egress) < rankDeEgress(eg) {
			divergencias = append(divergencias, fmt.Sprintf("tool %q: o snapshot declara egress %q e o nó %q — o snapshot não pode declarar menos risco do que o nó", c.Name, c.Egress.String(), t.Egress))
		}
		if rev, ok := reversibilidades[t.Reversibility]; !ok {
			divergencias = append(divergencias, fmt.Sprintf("tool %q: o nó declara reversibility %q, que este aos-orq não reconhece", c.Name, t.Reversibility))
		} else if !c.Reversibility.IsIrreversible() && rev.IsIrreversible() {
			divergencias = append(divergencias, fmt.Sprintf("tool %q: o snapshot declara %q e o nó %q — o snapshot não pode declarar menos risco do que o nó", c.Name, c.Reversibility.String(), t.Reversibility))
		}
	}
	if len(divergencias) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d divergência(s): %s", ErrSnapshotDivergeDoNo, len(divergencias), strings.Join(divergencias, "; "))
}

// rankDeEgress ordena o eixo de egress pelo risco, com o desconhecido no topo (fail-closed, como
// `risk.Egress.IsExternal`).
func rankDeEgress(e risk.Egress) int {
	switch e {
	case risk.EgressNone:
		return 0
	case risk.EgressInternal:
		return 1
	default:
		return 2
	}
}

package bancoensaio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// AS TOOLS DO BANCO. São FALSAS e LOCAIS: devolvem um documento da bateria ou confirmam o que
// receberam. Nenhuma tem efeito externo — não escrevem em disco, não abrem rede, não guardam
// nada. São registadas no Reference Monitor do nó de ensaio, e é só por ele que correm
// ([referencemonitor.Monitor.Mediate]), como em produção.

// ToolDoBanco é o schema de uma tool do banco, tal como é oferecida ao modelo.
type ToolDoBanco struct {
	Nome       string
	Descricao  string
	Parametros json.RawMessage
}

// Os nomes das tools do banco.
const (
	// ToolArquivo lê um documento da bateria pelo nome. É o nome que os casos de wire do
	// AOS-508 usam nas suas tool calls.
	ToolArquivo = "arquivo"
	// ToolGuardar recebe um texto e confirma quantos bytes recebeu. Não guarda nada.
	ToolGuardar = "guardar"
)

// ToolsDoBanco devolve as tools do banco, numa ordem fixa.
func ToolsDoBanco() []ToolDoBanco {
	return []ToolDoBanco{
		{
			Nome:       ToolArquivo,
			Descricao:  "Reads one test document by its file name and returns its text.",
			Parametros: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File name of the document, for example notas-armazem.txt"}},"required":["path"],"additionalProperties":false}`),
		},
		{
			Nome:       ToolGuardar,
			Descricao:  "Receives a text and confirms how many bytes it received. It stores nothing.",
			Parametros: json.RawMessage(`{"type":"object","properties":{"texto":{"type":"string","description":"The text to hand over"}},"required":["texto"],"additionalProperties":false}`),
		},
	}
}

// digestDaTool é o digest pinado de uma tool do banco: o do seu schema.
func digestDaTool(t ToolDoBanco) string {
	soma := sha256.Sum256([]byte(t.Nome + "\n" + t.Descricao + "\n" + string(t.Parametros)))
	return "sha256:" + hex.EncodeToString(soma[:])
}

// errDocumentoDesconhecido — a tool de leitura só conhece os documentos da bateria.
var errDocumentoDesconhecido = errors.New("documento desconhecido: so existem os documentos de teste da bateria")

// registarTools regista as tools do banco no Reference Monitor do nó de ensaio.
func registarTools(mon *referencemonitor.Monitor, b *Bateria) error {
	arquivo := func(_ context.Context, input []byte) ([]byte, error) {
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, errors.New("argumentos ilegiveis: esperava {\"path\": \"<nome>\"}")
		}
		// O nome é procurado TAL E QUAL no mapa dos documentos embebidos: não há caminho de
		// ficheiro, não há disco, e um nome com separadores simplesmente não existe.
		doc, ok := b.Documento(args.Path)
		if !ok {
			return nil, errDocumentoDesconhecido
		}
		return doc, nil
	}
	guardar := func(_ context.Context, input []byte) ([]byte, error) {
		var args struct {
			Texto string `json:"texto"`
		}
		if err := json.Unmarshal(input, &args); err != nil {
			return nil, errors.New("argumentos ilegiveis: esperava {\"texto\": \"...\"}")
		}
		return []byte(fmt.Sprintf("recebido: %d bytes", len(args.Texto))), nil
	}
	if err := mon.Register(ToolArquivo, arquivo); err != nil {
		return err
	}
	return mon.Register(ToolGuardar, guardar)
}

// recusaDeDocumento é o hook do Reference Monitor do nó de ensaio que NEGA a leitura de um
// documento num run (o caso da tool negada, T5). A recusa é do RM, antes do despacho: a tool
// não corre. Quem declara o que é negado é o caso da bateria, nunca conteúdo do modelo.
type recusaDeDocumento struct {
	mu     sync.Mutex
	porRun map[string]string
}

func novaRecusaDeDocumento() *recusaDeDocumento {
	return &recusaDeDocumento{porRun: map[string]string{}}
}

// negar declara que o run não pode ler o documento.
func (r *recusaDeDocumento) negar(runID, documento string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.porRun[runID] = documento
}

// Name implementa [referencemonitor.Hook].
func (r *recusaDeDocumento) Name() string { return "banco-ensaio-recusa-de-documento" }

// Evaluate implementa [referencemonitor.Hook].
func (r *recusaDeDocumento) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	r.mu.Lock()
	negado := r.porRun[call.RunID]
	r.mu.Unlock()
	if negado == "" || call.ToolID != ToolArquivo {
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	var args struct {
		Path string `json:"path"`
	}
	// Argumentos ilegíveis não chegam a nomear o documento negado: seguem, e a tool recusa-os.
	if json.Unmarshal(call.Input, &args) == nil && strings.TrimSpace(args.Path) == negado {
		return referencemonitor.HookResult{
			Decision: referencemonitor.HookDeny,
			Reason:   "documento reservado neste run do banco de ensaio",
		}, nil
	}
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

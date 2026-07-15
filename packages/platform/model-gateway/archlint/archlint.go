// Package archlint é o analisador de arquitectura do no-bypass do Model Gateway
// (AOS-055, tecnica/06 §3, critério "nenhuma invocação directa de provider fora
// do GW"). Detecta, por análise sintáctica (go/ast, go/parser, go/token —
// stdlib pura, offline), qualquer caminho de código FORA do model-gateway que
// fale com um provedor de LLM directamente:
//
//  1. IMPORTA um SDK de provedor conhecido (openai-go, go-openai, anthropic-sdk,
//     google genai, …); ou
//  2. Referencia um ENDPOINT de provedor por string literal (api.openai.com,
//     api.anthropic.com, generativelanguage.googleapis.com, …).
//
// À imagem do archlint do Reference Monitor (AOS-003), corre como TESTE Go e é
// uma HEURÍSTICA de defesa-em-profundidade, NÃO uma prova. A garantia forte é
// ESTRUTURAL: o adaptador de provider vive atrás da porta [port.Gateway] e é o
// único componente que fala HTTP com um provedor; um consumidor só tem a porta.
// O lint apanha os enganos óbvios (um import de SDK, um endpoint hard-coded fora
// do GW), contornável por ofuscação — a estrutura é a primeira camada, isto a
// segunda.
//
// # Isenção do próprio GW
//
// O adaptador REAL do GW (internal/adapters/openai_http.go) fala HTTP com o
// provedor por desenho — é o gate, e vive sob internal/ para que NENHUM módulo
// externo o possa importar (garantia estrutural). [AnalyzeDir] com
// [Options.IsGateway] a true (ou os caminhos sob model-gateway/) é ISENTO: a
// regra proíbe o acesso directo FORA do GW.
package archlint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// forbiddenImportSubstrings são fragmentos de import path proibidos FORA do GW.
// Cobre (a) SDKs de provedor conhecidos e (b) o pacote de adaptadores do PRÓPRIO
// GW — a garantia forte é estrutural (os adaptadores vivem sob
// model-gateway/internal/, logo não são importáveis de fora do módulo), mas o
// lint apanha em defesa-em-profundidade qualquer tentativa de referenciar o
// pacote de adaptadores do GW (concreto + [NewCredential]) directamente,
// contornando a porta [port.Gateway].
var forbiddenImportSubstrings = []string{
	"github.com/sashabaranov/go-openai",
	"github.com/openai/openai-go",
	"github.com/anthropics/anthropic-sdk-go",
	"google.golang.org/genai",
	"github.com/google/generative-ai-go",
	"cloud.google.com/go/vertexai",
	"github.com/cohere-ai/cohere-go",
	// Adaptadores do próprio GW: importá-los fora do GW é bypass da porta.
	"platform/model-gateway/adapters",
	"platform/model-gateway/internal/adapters",
}

// forbiddenEndpointSubstrings são hosts de provedor. Uma string literal que os
// contenha, fora do GW, denota uma chamada directa ao endpoint do provedor.
var forbiddenEndpointSubstrings = []string{
	"api.openai.com",
	"api.anthropic.com",
	"generativelanguage.googleapis.com",
	"api.cohere.ai",
	"api.mistral.ai",
}

// Violation descreve uma infracção da regra de no-bypass do GW.
type Violation struct {
	File    string
	Line    int
	Col     int
	Kind    string // "provider-sdk-import" | "provider-endpoint-literal"
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d:%d: %s (%s)", v.File, v.Line, v.Col, v.Message, v.Kind)
}

// Options configura a análise.
type Options struct {
	// IsGateway isenta o directório: é código DO próprio GW (o adaptador de
	// provider fala com o provedor por desenho).
	IsGateway bool
	// IncludeTests também analisa ficheiros _test.go (default: ignora-os).
	IncludeTests bool
}

// AnalyzeDir analisa os ficheiros .go (não-recursivo) do directório e devolve as
// violações, ordenadas por posição. Se opts.IsGateway, devolve sempre vazio (o
// GW é a excepção legítima).
func AnalyzeDir(dir string, opts Options) ([]Violation, error) {
	if opts.IsGateway {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var violations []Violation
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if !opts.IncludeTests && strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		violations = append(violations, analyzeFile(fset, path, file)...)
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})
	return violations, nil
}

// AnalyzeTree percorre RECURSIVAMENTE uma árvore de packages e agrega as
// violações. Os directórios cujo caminho contém gwPathMarker (ex.:
// "model-gateway") são ISENTOS (código do próprio GW). É o modo usado para
// varrer os planos consumidores (Agent Runtime, control-plane, …) e provar que
// nenhum invoca um provider directamente.
func AnalyzeTree(root, gwPathMarker string, opts Options) ([]Violation, error) {
	var all []Violation
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		base := d.Name()
		if base == "testdata" || base == ".git" || base == "vendor" {
			return filepath.SkipDir
		}
		dirOpts := opts
		if gwPathMarker != "" && hasPathSegment(path, gwPathMarker) {
			dirOpts.IsGateway = true
		}
		vs, aerr := AnalyzeDir(path, dirOpts)
		if aerr != nil {
			return aerr
		}
		all = append(all, vs...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		return all[i].Line < all[j].Line
	})
	return all, nil
}

func analyzeFile(fset *token.FileSet, path string, file *ast.File) []Violation {
	var violations []Violation

	// 1) Imports de SDK de provedor.
	for _, imp := range file.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		for _, bad := range forbiddenImportSubstrings {
			if strings.Contains(p, bad) {
				violations = append(violations, mkViolation(fset, path, imp.Pos(), "provider-sdk-import",
					fmt.Sprintf("import de SDK de provider %q fora do GW; use a porta do Model Gateway", p)))
			}
		}
	}

	// 2) Endpoints de provedor em string literals.
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val := strings.ToLower(strings.Trim(lit.Value, "`\""))
		for _, host := range forbiddenEndpointSubstrings {
			if strings.Contains(val, host) {
				violations = append(violations, mkViolation(fset, path, lit.Pos(), "provider-endpoint-literal",
					fmt.Sprintf("endpoint de provider %q referenciado directamente fora do GW", host)))
			}
		}
		return true
	})

	return violations
}

// hasPathSegment reporta se algum ELEMENTO de caminho de path é exactamente seg.
// Isenta a raiz do módulo GW por fronteira de segmento (ex.: ".../model-gateway"
// ou qualquer subdirectório), NUNCA por substring solta — assim um pacote
// consumidor sob um caminho que meramente CONTÉM o marcador (ex.:
// ".../foo-model-gateway-client/") não é isentado por engano das verificações
// de no-bypass.
func hasPathSegment(path, seg string) bool {
	for _, e := range strings.Split(filepath.ToSlash(path), "/") {
		if e == seg {
			return true
		}
	}
	return false
}

func mkViolation(fset *token.FileSet, path string, pos token.Pos, kind, msg string) Violation {
	p := fset.Position(pos)
	return Violation{File: path, Line: p.Line, Col: p.Column, Kind: kind, Message: msg}
}

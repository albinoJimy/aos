package attestation

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CONFORMIDADE COM A PORTA — o preço do isolamento da dependência, cobrado por um teste.
//
// Este módulo NÃO pode importar packages/integration nem ser importado por ele: é essa
// separação que mantém o CBOR fora do grafo de build do nó (Carta emenda 1.3). A
// consequência é que o compilador NUNCA verifica que [Verifier] satisfaz
// integration.DeviceAttestationVerifier — a garantia habitual (`var _ I = (*T)(nil)`) não
// está disponível sem criar exactamente a aresta que queremos proibir.
//
// Em vez de confiar na prosa, este teste LÊ o ficheiro da porta, extrai a assinatura
// declarada e compara-a com a que este pacote implementa. Se alguém mudar o contrato de um
// lado, o outro fica vermelho — o desacoplamento deixa de ter o custo de silêncio.

// portFile é o ficheiro que declara a porta, relativo à raiz deste módulo.
const portFile = "../../integration/device_attestation.go"

// portInterfaceName / portMethodName identificam o que se procura no ficheiro.
const (
	portInterfaceName = "DeviceAttestationVerifier"
	portMethodName    = "VerifyDeviceAttestation"
)

// wantSignature é a assinatura CANÓNICA do método da porta, em tipos STDLIB puros. Se este
// valor tiver de mudar, é porque o contrato mudou — e a mudança tem de ser deliberada nos
// dois módulos.
const wantSignature = "(context.Context, []byte, []byte, []byte) ([]byte, error)"

// localPort é a porta REDECLARADA localmente com a assinatura acima. A afirmação de
// conformidade em tempo de compilação faz-se contra ELA; o teste garante que ela é
// byte-a-byte o que packages/integration declara.
type localPort interface {
	VerifyDeviceAttestation(ctx context.Context, attestationObject, clientDataJSON, expectedChallenge []byte) (deviceID []byte, err error)
}

// Conformidade estrutural em tempo de COMPILAÇÃO contra a redeclaração local.
var _ localPort = (*Verifier)(nil)

func TestPortConformance_MatchesIntegrationPort(t *testing.T) {
	path, err := filepath.Abs(portFile)
	if err != nil {
		t.Fatalf("resolver o caminho da porta: %v", err)
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler a porta %s: %v — o contrato tem de estar acessível para ser guardado", path, err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse da porta: %v", err)
	}

	sig, found := findInterfaceMethodSignature(fset, f, portInterfaceName, portMethodName)
	if !found {
		t.Fatalf("não encontrei %s.%s em %s — a porta foi renomeada/removida sem actualizar o implementador", portInterfaceName, portMethodName, portFile)
	}
	if sig != wantSignature {
		t.Fatalf("a porta declara %s%s\nmas este módulo implementa %s%s\n(o contrato divergiu: actualizar os dois lados deliberadamente)",
			portMethodName, sig, portMethodName, wantSignature)
	}

	// A porta tem de ser STDLIB PURA: se algum dia importar cbor/webauthn, o isolamento
	// morre na origem (o módulo do nó importa `integration`).
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(p, "cbor") || strings.Contains(p, "webauthn") || strings.Contains(p, "float16") {
			t.Errorf("o ficheiro da porta importa %q — a porta TEM de ser stdlib pura (Carta emenda 1.3)", p)
		}
	}
}

// findInterfaceMethodSignature devolve a assinatura canónica "(params) (results)" do método
// pedido, dentro da interface pedida.
func findInterfaceMethodSignature(fset *token.FileSet, f *ast.File, ifaceName, methodName string) (string, bool) {
	var out string
	var found bool
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != ifaceName {
			return true
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return false
		}
		for _, m := range iface.Methods.List {
			if len(m.Names) != 1 || m.Names[0].Name != methodName {
				continue
			}
			ft, ok := m.Type.(*ast.FuncType)
			if !ok {
				continue
			}
			out = "(" + strings.Join(fieldTypes(fset, ft.Params), ", ") + ") (" + strings.Join(fieldTypes(fset, ft.Results), ", ") + ")"
			found = true
		}
		return false
	})
	return out, found
}

// fieldTypes expande uma lista de campos em tipos, repetindo o tipo por cada nome declarado
// (`a, b []byte` ⇒ ["[]byte", "[]byte"]) — é a forma canónica que torna a comparação
// insensível a como os parâmetros foram agrupados.
func fieldTypes(fset *token.FileSet, fl *ast.FieldList) []string {
	if fl == nil {
		return nil
	}
	var out []string
	for _, fld := range fl.List {
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, fld.Type); err != nil {
			out = append(out, "<erro>")
			continue
		}
		n := len(fld.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, buf.String())
		}
	}
	return out
}

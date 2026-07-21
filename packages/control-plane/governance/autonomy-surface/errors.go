package autonomysurface

import "errors"

// ErrNilLevelReader — construção de uma [Surface] sem [LevelReader]. A superfície LÊ
// o nível corrente e o histórico daqui; sem ele não há nada a apresentar.
var ErrNilLevelReader = errors.New("autonomysurface: superficie exige um LevelReader")

// ErrNoReviewer — [Surface.RequestMoreAutonomy] sem [LevelReviewer] configurado. A
// superfície NÃO decide o nível: sem a porta de decisão (o adaptador sobre o
// Controller de AOS-090) não há a quem DELEGAR o pedido de revisão (AC4), pelo que o
// pedido é recusado — fail-closed, nunca uma auto-promoção pela superfície.
var ErrNoReviewer = errors.New("autonomysurface: pedido de revisao sem LevelReviewer (a superficie nao decide o nivel)")

package model

// Совместимо с форматом Loki JSON push:
// {
//   "streams":[
//     {
//       "stream":{"label":"value"},
//       "values":[["<unix_ns_string>","line"], ...]
//     }
//   ]
// }

type PushRequest struct {
	Streams []Stream `json:"streams"`
}

type Stream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"` // [ timestamp(ns as string), line ]
}

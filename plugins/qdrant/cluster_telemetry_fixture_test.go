package main

// What a Qdrant 1.19.1 three-peer cluster (peers dbwp-r12plug-qd1, qd2 and qd3, the
// leader being qd1) answered when asked at qd2, a follower. A test that
// changes a captured value says so; nothing else here is made up.
const (
	// /cluster at qd2.
	fxClusterAtFollower = `{"result":{"status":"enabled","peer_id":3702969928492873,"peers":{"333551949039778":{"uri":"http://dbwp-r12plug-qd3:6335/"},"3702969928492873":{"uri":"http://dbwp-r12plug-qd2:6335/"},"1575883389625398":{"uri":"http://dbwp-r12plug-qd1:6335/"}},"raft_info":{"term":1,"commit":13,"pending_operations":0,"leader":1575883389625398,"role":"Follower","is_voter":true},"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-10-03T17:29:36.396855256Z"},"message_send_failures":{}},"status":"ok","time":0.000019541}`

	// /cluster/telemetry at qd2 with all three peers up.
	fxTelemetryUp = `{"result":{"collections":{"c1":{"id":"c1"}},"cluster":{"enabled":true,"number_of_peers":3,"peers":{"3702969928492873":{"uri":"http://dbwp-r12plug-qd2:6335/","responsive":true,"details":{"version":"1.19.1","role":"Follower","is_voter":true,"term":1,"commit":13,"num_pending_operations":0,"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-10-03T17:29:36.396Z"}}},"333551949039778":{"uri":"http://dbwp-r12plug-qd3:6335/","responsive":true,"details":{"version":"1.19.1","role":"Follower","is_voter":true,"term":1,"commit":13,"num_pending_operations":0,"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-10-03T17:29:36.397Z"}}},"1575883389625398":{"uri":"http://dbwp-r12plug-qd1:6335/","responsive":true,"details":{"version":"1.19.1","role":"Leader","is_voter":true,"term":1,"commit":13,"num_pending_operations":0,"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-10-03T17:29:36.383Z"}}}}}},"status":"ok","time":0.004353625}`

	// The same with qd3 stopped: it is not responsive and reports no details.
	fxTelemetryDown = `{"result":{"collections":{"c1":{"id":"c1"}},"cluster":{"enabled":true,"number_of_peers":3,"peers":{"3702969928492873":{"uri":"http://dbwp-r12plug-qd2:6335/","responsive":true,"details":{"version":"1.19.1","role":"Follower","is_voter":true,"term":1,"commit":13,"num_pending_operations":0,"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-10-03T17:29:25.764Z"}}},"333551949039778":{"uri":"http://dbwp-r12plug-qd3:6335/","responsive":false,"details":null},"1575883389625398":{"uri":"http://dbwp-r12plug-qd1:6335/","responsive":true,"details":{"version":"1.19.1","role":"Leader","is_voter":true,"term":1,"commit":13,"num_pending_operations":0,"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-10-03T17:29:25.752Z"}}}}}},"status":"ok","time":0.46099075}`
)

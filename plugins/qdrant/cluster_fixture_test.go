package main

// What a Qdrant 1.19.1 three-peer cluster answered, as captured from three
// containers on one network (peers dbwp-oth-qd1, qd2 and qd3, this one being
// qd1, the leader). A test that changes a captured value says so; nothing
// else here is made up.

const (
	fxRoot        = `{"title":"qdrant - vector search engine","version":"1.19.1","commit":"6ab21cac18ebb6f4ae29102c7f8f5cc11affd5de"}`
	fxCollections = `{"result":{"collections":[{"name":"docs3"}]},"status":"ok","time":9.833e-6}`
	fxInfo        = `{"result":{"status":"green","optimizer_status":"ok","indexed_vectors_count":0,"points_count":16,"segments_count":12,"config":{"params":{"vectors":{"size":4,"distance":"Cosine"},"shard_number":3,"replication_factor":3,"write_consistency_factor":1,"on_disk_payload":true},"hnsw_config":{"m":16,"ef_construct":100,"full_scan_threshold":10000,"max_indexing_threads":0,"on_disk":false},"optimizer_config":{"deleted_threshold":0.2,"vacuum_min_vector_number":1000,"default_segment_number":0,"max_segment_size":null,"memmap_threshold":null,"indexing_threshold":10000,"flush_interval_sec":5,"max_optimization_threads":null,"prevent_unoptimized":null},"wal_config":{"wal_capacity_mb":32,"wal_segments_ahead":0,"wal_retain_closed":1},"quantization_config":null},"payload_schema":{},"update_queue":{"length":0}},"status":"ok","time":0.000507875}`

	// All three peers up: no send failures.
	fxClusterHealthy = `{"result":{"status":"enabled","peer_id":5520940345939595,"peers":{"1212972222451972":{"uri":"http://dbwp-oth-qd3:6335/"},"5520940345939595":{"uri":"http://dbwp-oth-qd1:6335/"},"5204490162315831":{"uri":"http://dbwp-oth-qd2:6335/"}},"raft_info":{"term":1,"commit":26,"pending_operations":0,"leader":5520940345939595,"role":"Leader","is_voter":true},"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-10-03T00:30:54.401800253Z"},"message_send_failures":{}},"status":"ok","time":0.0000255}`

	// qd3 stopped: this peer has been failing to message it since.
	fxClusterPeerDown = `{"result":{"status":"enabled","peer_id":5520940345939595,"peers":{"5520940345939595":{"uri":"http://dbwp-oth-qd1:6335/"},"5204490162315831":{"uri":"http://dbwp-oth-qd2:6335/"},"1212972222451972":{"uri":"http://dbwp-oth-qd3:6335/"}},"raft_info":{"term":1,"commit":38,"pending_operations":0,"leader":5520940345939595,"role":"Leader","is_voter":true},"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-10-03T00:31:04.507558633Z"},"message_send_failures":{"http://dbwp-oth-qd3:6335/":{"count":18,"latest_error":"Error in closure supplied to transport channel pool: code: 'The service is currently unavailable', message: \"Failed to connect to http://dbwp-oth-qd3:6335/, error: transport error\"","latest_error_timestamp":"2026-10-03T00:31:04.516454383Z"}}},"status":"ok","time":0.000025167}`

	// The same peer as a follower, which is what qd2 answered.
	fxClusterFollower = `{"result":{"status":"enabled","peer_id":5204490162315831,"peers":{"5204490162315831":{"uri":"http://dbwp-oth-qd2:6335/"},"5520940345939595":{"uri":"http://dbwp-oth-qd1:6335/"}},"raft_info":{"term":1,"commit":6,"pending_operations":0,"leader":5520940345939595,"role":"Follower","is_voter":true},"consensus_thread_status":{"consensus_thread_status":"working","last_update":"2026-10-02T23:53:40.810466428Z"},"message_send_failures":{}},"status":"ok","time":0.000023208}`

	fxClusterDisabled = `{"result":{"status":"disabled"},"status":"ok","time":2.542e-6}`

	// The collection while all nine replicas serve.
	fxShardsActive = `{"result":{"peer_id":5520940345939595,"shard_count":3,"local_shards":[{"shard_id":0,"points_count":0,"state":"Active"},{"shard_id":1,"points_count":0,"state":"Active"},{"shard_id":2,"points_count":4,"state":"Active"}],"remote_shards":[{"shard_id":0,"peer_id":5204490162315831,"state":"Active"},{"shard_id":0,"peer_id":1212972222451972,"state":"Active"},{"shard_id":1,"peer_id":1212972222451972,"state":"Active"},{"shard_id":1,"peer_id":5204490162315831,"state":"Active"},{"shard_id":2,"peer_id":5204490162315831,"state":"Active"},{"shard_id":2,"peer_id":1212972222451972,"state":"Active"}],"shard_transfers":[]},"status":"ok","time":0.000231625}`

	// After writes landed on shards 0 and 2 while qd3 was down: the replicas
	// that missed them are Dead, and shard 1, which was not written, is not.
	fxShardsDead = `{"result":{"peer_id":5520940345939595,"shard_count":3,"local_shards":[{"shard_id":0,"points_count":2,"state":"Active"},{"shard_id":1,"points_count":0,"state":"Active"},{"shard_id":2,"points_count":6,"state":"Active"}],"remote_shards":[{"shard_id":0,"peer_id":5204490162315831,"state":"Active"},{"shard_id":0,"peer_id":1212972222451972,"state":"Dead"},{"shard_id":1,"peer_id":1212972222451972,"state":"Active"},{"shard_id":1,"peer_id":5204490162315831,"state":"Active"},{"shard_id":2,"peer_id":5204490162315831,"state":"Active"},{"shard_id":2,"peer_id":1212972222451972,"state":"Dead"}],"shard_transfers":[]},"status":"ok","time":0.000302708}`

	// qd3 coming back: shard 1 is being rebuilt from qd2 by a snapshot
	// transfer while shard 2 has not started.
	fxShardsRecovering = `{"result":{"peer_id":5520940345939595,"shard_count":3,"local_shards":[{"shard_id":0,"points_count":2,"state":"Active"},{"shard_id":1,"points_count":2,"state":"Active"},{"shard_id":2,"points_count":8,"state":"Active"}],"remote_shards":[{"shard_id":0,"peer_id":5204490162315831,"state":"Active"},{"shard_id":0,"peer_id":1212972222451972,"state":"Active"},{"shard_id":1,"peer_id":1212972222451972,"state":"Recovery"},{"shard_id":1,"peer_id":5204490162315831,"state":"Active"},{"shard_id":2,"peer_id":5204490162315831,"state":"Active"},{"shard_id":2,"peer_id":1212972222451972,"state":"Dead"}],"shard_transfers":[{"shard_id":1,"from":5204490162315831,"to":1212972222451972,"sync":true,"method":"snapshot"}]},"status":"ok","time":0.000232875}`

	// A standalone instance's own collection: one shard, no remote.
	fxShardsSolo = `{"result":{"peer_id":3391423248779018,"shard_count":1,"local_shards":[{"shard_id":0,"points_count":0,"state":"Active"}],"remote_shards":[],"shard_transfers":[]},"status":"ok","time":0.0000665}`
)

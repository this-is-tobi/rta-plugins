package main

// Captured from a six-node redis 7.4.11 cluster, three primaries with one replica each, after a
// replica was paused and 200 keys were written to its shard: that replica is flagged fail by the
// others, and its offset has stood still while its primary's moved.

const clusterShardsReply = "*3\r\n*4\r\n$5\r\nslots\r\n*2\r\n:10923\r\n:16383\r\n$5\r\nnodes\r\n*2\r\n*14\r\n$2\r\nid\r\n$40\r\n" +
	"1dad7543ba671c20d4563d65765bb4860a6a50c2\r\n$4\r\nport\r\n:6379\r\n$2\r\nip\r\n$11\r\n172.20.0.14\r" +
	"\n$8\r\nendpoint\r\n$11\r\n172.20.0.14\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$18\r\nreplication-offset\r\n" +
	":12837\r\n$6\r\nhealth\r\n$6\r\nonline\r\n*14\r\n$2\r\nid\r\n$40\r\na3de9faaed080da99e56d2d8a242fd78" +
	"b02503ca\r\n$4\r\nport\r\n:6379\r\n$2\r\nip\r\n$11\r\n172.20.0.15\r\n$8\r\nendpoint\r\n$11\r\n172.20" +
	".0.15\r\n$4\r\nrole\r\n$7\r\nreplica\r\n$18\r\nreplication-offset\r\n:211\r\n$6\r\nhealth\r\n$4\r\nf" +
	"ail\r\n*4\r\n$5\r\nslots\r\n*2\r\n:5461\r\n:10922\r\n$5\r\nnodes\r\n*2\r\n*14\r\n$2\r\nid\r\n$40\r\n" +
	"d11b40aa7a95de9edcdbbbd9031f5301c9b942be\r\n$4\r\nport\r\n:6379\r\n$2\r\nip\r\n$11\r\n172.20.0.13\r" +
	"\n$8\r\nendpoint\r\n$11\r\n172.20.0.13\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$18\r\nreplication-offset\r\n" +
	":317\r\n$6\r\nhealth\r\n$6\r\nonline\r\n*14\r\n$2\r\nid\r\n$40\r\n4356bd41a21714ea7d3f00572a9407394c" +
	"d7e546\r\n$4\r\nport\r\n:6379\r\n$2\r\nip\r\n$11\r\n172.20.0.17\r\n$8\r\nendpoint\r\n$11\r\n172.20.0" +
	".17\r\n$4\r\nrole\r\n$7\r\nreplica\r\n$18\r\nreplication-offset\r\n:317\r\n$6\r\nhealth\r\n$6\r\nonl" +
	"ine\r\n*4\r\n$5\r\nslots\r\n*2\r\n:0\r\n:5460\r\n$5\r\nnodes\r\n*2\r\n*14\r\n$2\r\nid\r\n$40\r\ndbf5" +
	"8b4166dcb938d8b53789ba6d667a77069afa\r\n$4\r\nport\r\n:6379\r\n$2\r\nip\r\n$11\r\n172.20.0.12\r\n$8" +
	"\r\nendpoint\r\n$11\r\n172.20.0.12\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$18\r\nreplication-offset\r\n:315" +
	"\r\n$6\r\nhealth\r\n$6\r\nonline\r\n*14\r\n$2\r\nid\r\n$40\r\n4f69015b334128e6c472a00b1d985eea27d3c4" +
	"1a\r\n$4\r\nport\r\n:6379\r\n$2\r\nip\r\n$11\r\n172.20.0.16\r\n$8\r\nendpoint\r\n$11\r\n172.20.0.16" +
	"\r\n$4\r\nrole\r\n$7\r\nreplica\r\n$18\r\nreplication-offset\r\n:315\r\n$6\r\nhealth\r\n$6\r\nonline" +
	"\r\n"

const clusterInfoReply = "$556\r\ncluster_state:ok\r\ncluster_slots_assigned:16384\r\ncluster_slots_ok:16384\r\ncluster_slots_" +
	"pfail:0\r\ncluster_slots_fail:0\r\ncluster_known_nodes:6\r\ncluster_size:3\r\ncluster_current_epoch:" +
	"6\r\ncluster_my_epoch:1\r\ncluster_stats_messages_ping_sent:368\r\ncluster_stats_messages_pong_sent:" +
	"369\r\ncluster_stats_messages_sent:737\r\ncluster_stats_messages_ping_received:364\r\ncluster_stats_" +
	"messages_pong_received:357\r\ncluster_stats_messages_meet_received:5\r\ncluster_stats_messages_fail_" +
	"received:1\r\ncluster_stats_messages_received:727\r\ntotal_cluster_links_buffer_limit_exceeded:0\r\n" +
	"\r\n"

const clusterNodesText = "" +
	"1dad7543ba671c20d4563d65765bb4860a6a50c2 172.20.0.14:6379@16379 master - 0 1790985722988 3 connected 10923-16383\n" +
	"d11b40aa7a95de9edcdbbbd9031f5301c9b942be 172.20.0.13:6379@16379 master - 0 1790985723089 2 connected 5461-10922\n" +
	"dbf58b4166dcb938d8b53789ba6d667a77069afa 172.20.0.12:6379@16379 myself,master - 0 0 1 connected 0-5460\n" +
	"4f69015b334128e6c472a00b1d985eea27d3c41a 172.20.0.16:6379@16379 slave dbf58b4166dcb938d8b53789ba6d667a77069afa 0 1790985724001 1 connected\n" +
	"a3de9faaed080da99e56d2d8a242fd78b02503ca 172.20.0.15:6379@16379 slave,fail 1dad7543ba671c20d4563d65765bb4860a6a50c2 1790985693527 1790985692000 3 connected\n" +
	"4356bd41a21714ea7d3f00572a9407394cd7e546 172.20.0.17:6379@16379 slave d11b40aa7a95de9edcdbbbd9031f5301c9b942be 0 1790985723000 2 connected\n"

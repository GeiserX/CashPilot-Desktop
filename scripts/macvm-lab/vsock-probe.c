// Lab only: try to open a vsock connection from the macOS guest to the host.
//
// Build on the host:  clang -O2 -o vsock-probe vsock-probe.c
// Copy it into the guest and run:  vsock-probe <cid> <first-port> <last-port>
//   cid 2 is the host (VMADDR_CID_HOST). Prints one line per port that
//   connects, and a summary. A connect means a host process listens on that
//   port for this VM; refused means nobody does.
//
// Positive control: `vsock-probe listen <port>` listens in the guest. The host
// side of `tart exec` dials the guest, so the guest agent's own port must show
// up as a listener the host can reach (Tart uses port 8080).
#include <errno.h>
#include <limits.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/vsock.h>
#include <unistd.h>

static int try_port(unsigned cid, unsigned port) {
  int s = socket(AF_VSOCK, SOCK_STREAM, 0);
  if (s < 0) { perror("socket(AF_VSOCK)"); exit(2); }
  struct sockaddr_vm a;
  memset(&a, 0, sizeof a);
  a.svm_len = sizeof a;
  a.svm_family = AF_VSOCK;
  a.svm_cid = cid;
  a.svm_port = port;
  int r = connect(s, (struct sockaddr *)&a, sizeof a);
  int e = errno;
  close(s);
  return r == 0 ? 0 : e;
}

int main(int argc, char **argv) {
  if (argc == 3 && strcmp(argv[1], "listen") == 0) {
    int s = socket(AF_VSOCK, SOCK_STREAM, 0);
    struct sockaddr_vm a;
    memset(&a, 0, sizeof a);
    a.svm_len = sizeof a; a.svm_family = AF_VSOCK;
    a.svm_cid = VMADDR_CID_ANY; a.svm_port = (unsigned)atoi(argv[2]);
    if (bind(s, (struct sockaddr *)&a, sizeof a) || listen(s, 1)) { perror("bind/listen"); return 2; }
    printf("listening on vsock port %s\n", argv[2]);
    fflush(stdout);
    int c = accept(s, NULL, NULL);
    printf("accepted %d\n", c);
    return 0;
  }
  if (argc != 4) { fprintf(stderr, "usage: %s <cid> <first> <last> | listen <port>\n", argv[0]); return 2; }
  unsigned long v[3];
  for (int i = 0; i < 3; i++) {
    char *end; errno = 0;
    v[i] = strtoul(argv[i + 1], &end, 10);
    if (errno || end == argv[i + 1] || *end || strchr(argv[i + 1], '-') || v[i] > UINT_MAX) {
      fprintf(stderr, "not a number: %s\n", argv[i + 1]); return 2;
    }
  }
  unsigned cid = (unsigned)v[0], first = (unsigned)v[1], last = (unsigned)v[2];
  if (first > last) { fprintf(stderr, "first port %u is above last port %u\n", first, last); return 2; }
  unsigned open = 0, refused = 0, other = 0;
  int other_errno = 0;
  for (unsigned p = first; p <= last; p++) {
    int e = try_port(cid, p);
    if (e == 0) { printf("OPEN cid=%u port=%u\n", cid, p); open++; }
    else if (e == ECONNREFUSED || e == ECONNRESET) refused++;
    else { other++; other_errno = e; }
    if (p == last) break;
  }
  printf("summary cid=%u ports=%u-%u open=%u refused=%u other=%u", cid, first, last, open, refused, other);
  if (other) printf(" last_errno=%d(%s)", other_errno, strerror(other_errno));
  printf("\n");
  return open ? 1 : 0;
}

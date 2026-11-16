<!DOCTYPE html>
<html lang="en">
  <head>
    <meta charset="utf-8">
    <meta http-equiv="X-UA-Compatible" content="IE=edge">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="description" content="">
    <meta name="author" content="">
    <link rel="icon" href="/favicon.ico">

    <title>{{.Product}}</title>

    <link href="/css/bootstrap.min.css" rel="stylesheet">
    <link href="/css/ie10-viewport-bug-workaround.css" rel="stylesheet">
    <link href="/css/dashboard.css" rel="stylesheet">

    <script src="/js/ie-emulation-modes-warning.js"></script>

    <!--[if lt IE 9]>
      <script src="https://oss.maxcdn.com/html5shiv/3.7.3/html5shiv.min.js"></script>
      <script src="https://oss.maxcdn.com/respond/1.4.2/respond.min.js"></script>
    <![endif]-->
  </head>

  <body>

    <nav class="navbar navbar-inverse navbar-fixed-top">
      <div class="container-fluid">
        <div class="navbar-header">
          <button type="button" class="navbar-toggle collapsed" data-toggle="collapse" data-target="#navbar" aria-expanded="false" aria-controls="navbar">
            <span class="sr-only">Toggle navigation</span>
            <span class="icon-bar"></span>
            <span class="icon-bar"></span>
            <span class="icon-bar"></span>
          </button>
          <a class="navbar-brand" href="#">{{.Product}}</a>
        </div>
        <div id="navbar" class="navbar-collapse collapse">
          <ul class="nav navbar-nav navbar-right">
            <li><a href="/dashboard/domains">Domains</a></li>
            {{ if .Integrated }}<li class="active"><a href="/dashboard/stats">Statistics</a></li>{{ end }}
            <li><a href="/dashboard/users">Manage Users</a></li>
            <li><a href="/dashboard/logs">Log Files</a></li>
            <li><a href="/logout">Logout</a></li>
          </ul>
        </div>
      </div>
    </nav>

    <div class="container-fluid">
      <div class="row">
        <div class="col-sm-3 col-md-2 sidebar">
	      <ul class="nav nav-sidebar">
            <li><a href="/dashboard/domains">Domains</a></li>
            {{ if .Integrated }}<li class="active"><a href="/dashboard/stats">Statistics</a></li>{{ end }}
            <li><a href="/dashboard/users">Manage Users</a></li>
            <li><a href="/dashboard/logs">Log Files</a></li>
          </ul>
          <ul class="nav nav-sidebar">
            <li><a href="/logout">Logout</a></li>
          </ul>
        </div>

        <div class="col-sm-9 col-sm-offset-3 col-md-10 col-md-offset-2 main">
          <h2 class="sub-header">Statistics</h2>

          {{ if .ErrorMessage }}<div>{{ .ErrorMessage }}</div>{{ end }}

          {{ with .Stats }}

          <div class="ds-toolbar">
            <span class="ds-count">DirectSlave {{ .Version }} &nbsp;&middot;&nbsp; taken {{ .Taken }} &nbsp;&middot;&nbsp; uptime {{ .Uptime }} &nbsp;&middot;&nbsp; {{ .Backend }}</span>
            <span class="ds-right">
              <label class="ds-auto"><input type="checkbox" id="autoRefresh"> auto <span id="autoLeft"></span></label>
              <a class="btn btn-default btn-xs" id="refreshNow" href="/dashboard/stats">
                <span class="glyphicon glyphicon-refresh"></span> Refresh
              </a>
            </span>
          </div>

          <div class="row ds-cards">
            <div class="col-sm-6">
              <div class="panel panel-default">
                <div class="panel-heading">Zones</div>
                <table class="table table-condensed ds-kv">
                  <tr>
                    <td>declared in control file</td>
                    <td class="text-right"><a href="{{ .AllURL }}">{{ .Declared }}</a></td>
                  </tr>
                  <tr>
                    <td>loaded in memory</td>
                    <td class="text-right">{{ .Loaded }}</td>
                  </tr>
                  <tr>
                    <td>DNSSEC signed</td>
                    <td class="text-right"><a href="{{ .SignedURL }}">{{ .Signed }}</a></td>
                  </tr>
                  {{ if .Partial }}
                  <tr class="danger">
                    <td colspan="2">
                      <a href="{{ .PartialURL }}">{{ .PartialText }}</a>
                      &mdash; a DNSKEY set with no signature over the SOA, or the other way round
                    </td>
                  </tr>
                  {{ end }}
                  {{ if .Missing }}
                  <tr class="warning">
                    <td colspan="2">
                      <a href="{{ .MissingURL }}">{{ .MissingText }}</a>
                      &mdash; a reload is pending or they failed to load
                    </td>
                  </tr>
                  {{ end }}
                </table>
              </div>
            </div>

            <div class="col-sm-6">
              <div class="panel panel-default">
                <div class="panel-heading">Totals</div>
                <table class="table table-condensed ds-kv">
                  {{ range .Lines }}
                  <tr><td>{{ .Label }}</td><td class="text-right">{{ .Value }}</td></tr>
                  {{ end }}
                </table>
              </div>
            </div>
          </div>

          <div class="panel panel-default">
            <div class="panel-heading">States &mdash; every count opens the matching zones</div>
            <table class="table table-condensed ds-states">
              <tbody>
              {{ range .States }}
                <tr class="{{ if .Zero }}ds-zero-row{{ end }}">
                  <td class="ds-state-count {{ .Class }}"><a href="{{ .URL }}">{{ .Count }}</a></td>
                  <td class="ds-state-name"><a href="{{ .URL }}">{{ .Name }}</a></td>
                  <td class="ds-state-meaning">{{ .Meaning }}</td>
                </tr>
              {{ end }}
              </tbody>
            </table>
          </div>

          {{ if .Masters }}
          <div class="panel panel-default">
            <div class="panel-heading">Primaries</div>
            <table class="table table-condensed table-striped">
              <thead>
                <tr>
                  <th>Primary</th>
                  <th class="text-right">Zones</th>
                  <th class="text-right">Unsynced</th>
                  <th class="text-right">Awaiting</th>
                  <th class="text-right">Records</th>
                </tr>
              </thead>
              <tbody>
              {{ range .Masters }}
                <tr>
                  <td><a href="{{ .MasterURL }}">{{ .Master }}</a></td>
                  <td class="text-right">{{ .Zones }}</td>
                  <td class="text-right">{{ .Unsynced }}</td>
                  <td class="text-right">{{ .Awaiting }}</td>
                  <td class="text-right">{{ .Records }}</td>
                </tr>
              {{ end }}
              </tbody>
            </table>
          </div>
          {{ end }}

          <div class="panel panel-default">
            <div class="panel-heading">
              <a data-toggle="collapse" href="#rawDump">Plain text, as <code>--dump-db stats</code> prints it</a>
            </div>
            <div id="rawDump" class="collapse">
              <pre class="ds-raw">{{ .Raw }}</pre>
            </div>
          </div>

          {{ end }}
        </div>
      </div>
    </div>

    <script src="/js/jquery.min.js"></script>
    <script>window.jQuery || document.write('<script src="/js/jquery.min.js"><\/script>')</script>
    <script src="/js/bootstrap.min.js"></script>
    <script src="/js/ie10-viewport-bug-workaround.js"></script>
    <script src="/js/ds-zones.js"></script>
  </body>
</html>

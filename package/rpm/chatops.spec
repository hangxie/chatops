Name:           chatops
Version:        CHANGEME
Release:        1%{?dist}
Summary:        ChatOps daemon and built-in MCP server
License:        BSD-3-Clause
Provides:       %{name} = %{version}
Source0:        %{name}-%{version}.tar.gz

%undefine source_date_epoch_from_changelog

%description
ChatOps daemon and built-in MCP server, for changelog visit https://github.com/hangxie/chatops/releases

%global debug_package %{nil}

%prep
%autosetup

%build
for bin in chatops chatops-mcp; do
    cp /tmp/${bin}.gz ${bin}.gz
    gunzip ${bin}.gz
done

%install
install -Dpm 0755 chatops %{buildroot}%{_bindir}/chatops
install -Dpm 0755 chatops-mcp %{buildroot}%{_bindir}/chatops-mcp

%files
%{_bindir}/chatops
%{_bindir}/chatops-mcp

using System;
using System.Runtime.CompilerServices;
using UnityEngine.Networking;

namespace OpenLeaderboard
{
    /// <summary>
    /// Lets you <c>await</c> a UnityWebRequest directly:
    /// <c>await request.SendWebRequest();</c>. Works on all Unity platforms
    /// (including WebGL/IL2CPP) because it hooks the request's completion on the
    /// Unity player loop rather than blocking a thread.
    /// </summary>
    public static class UnityWebRequestAwaiterExtensions
    {
        public static UnityWebRequestAwaiter GetAwaiter(this UnityWebRequestAsyncOperation op)
        {
            return new UnityWebRequestAwaiter(op);
        }
    }

    public class UnityWebRequestAwaiter : INotifyCompletion
    {
        private readonly UnityWebRequestAsyncOperation _op;
        private Action _continuation;

        public UnityWebRequestAwaiter(UnityWebRequestAsyncOperation op)
        {
            _op = op;
            _op.completed += _ =>
            {
                Action c = _continuation;
                _continuation = null;
                if (c != null) c();
            };
        }

        public bool IsCompleted => _op.isDone;

        public void GetResult() { }

        public void OnCompleted(Action continuation)
        {
            // If the op already finished (the completed event may have fired
            // before we registered), run immediately; otherwise store it.
            if (_op.isDone) continuation();
            else _continuation = continuation;
        }
    }
}
